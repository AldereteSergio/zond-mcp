// Package tools contiene la lógica de cada tool: validación contra los límites
// documentados por la API, armado del pedido y aplicación de las ventanas.
//
// La validación local no ahorra dinero —la API no cobra requests fallidos— pero
// ahorra un viaje de red y, en OCR, la subida de un archivo que iba a ser
// rechazado. Es ergonomía, no economía.
package tools

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"

	"github.com/AldereteSergio/zond-mcp/internal/faults"
)

// Límites documentados en api-search.txt.
const (
	QueryMinRunes       = 1
	QueryMaxRunes       = 128
	CountMin            = 1
	CountMax            = 50
	OriQuestionMaxRunes = 128
	RerankScoreMin      = 0
	RerankScoreMax      = 3
)

// runeLen cuenta caracteres Unicode, no bytes: la API dice "1 to 128 Unicode
// characters" y un query en chino tiene el triple de bytes que de caracteres.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// SearchParams es la entrada de la tool search.
//
// Los opcionales van como punteros para distinguir "no vino" de "vino cero": un
// campo ausente no debe viajar en el JSON. La API rechaza null en localization.
type SearchParams struct {
	Query          string
	Count          *int
	NeedContent    *bool
	OriQuestion    *string
	Localization   *string
	MinRerankScore *float64
}

// Validate revisa los límites antes de tocar la red (QA-010 a QA-014).
func (p SearchParams) Validate() *faults.Fault {
	if strings.TrimSpace(p.Query) == "" {
		return faults.InvalidInput("La query está vacía o son solo espacios.",
			"Mandá al menos un término de búsqueda.")
	}
	if runeLen(p.Query) > QueryMaxRunes {
		return faults.InvalidInput(
			fmt.Sprintf("La query tiene %d caracteres y el máximo es %d.", runeLen(p.Query), QueryMaxRunes),
			fmt.Sprintf("Recortala a %d caracteres o menos y volvé a intentar.", QueryMaxRunes))
	}
	if p.Count != nil && (*p.Count < CountMin || *p.Count > CountMax) {
		return faults.InvalidInput(
			fmt.Sprintf("count vale %d y el rango es 1 a 50.", *p.Count),
			"Usá un count entre 1 y 50. Para más cobertura conviene hacer varias queries distintas: la lista de una sola query rara vez pasa de diez URLs útiles.")
	}
	if p.OriQuestion != nil && (runeLen(*p.OriQuestion) < 1 || runeLen(*p.OriQuestion) > OriQuestionMaxRunes) {
		return faults.InvalidInput(
			fmt.Sprintf("ori_question debe tener entre 1 y %d caracteres.", OriQuestionMaxRunes),
			"Recortala o no la mandes.")
	}
	if p.MinRerankScore != nil && (*p.MinRerankScore < RerankScoreMin || *p.MinRerankScore > RerankScoreMax) {
		return faults.InvalidInput("min_rerank_score va de 0 a 3.",
			"Usá un valor entre 0 y 3; desde 1.5 la documentación considera el resultado de calidad.")
	}
	if p.Localization != nil && *p.Localization != "" {
		if net.ParseIP(*p.Localization) == nil {
			return faults.InvalidInput(
				fmt.Sprintf("%q no es una dirección IP.", *p.Localization),
				"localization espera una IP (IPv4 o IPv6) para localizar resultados, no un código de país.")
		}
	}
	return nil
}

// Payload arma el cuerpo JSON omitiendo los campos que no vinieron.
func (p SearchParams) Payload() ([]byte, *faults.Fault) {
	body := map[string]any{"query": p.Query}
	if p.Count != nil {
		body["count"] = *p.Count
	}
	if p.NeedContent != nil {
		body["need_content"] = *p.NeedContent
	}
	if p.OriQuestion != nil {
		body["ori_question"] = *p.OriQuestion
	}
	if p.Localization != nil && *p.Localization != "" {
		body["localization"] = *p.Localization
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, faults.Classify(faults.ToolSearch, 0, nil, err)
	}
	return raw, nil
}

// SearchResult es un resultado tal como lo devuelve la API, sin renombrar.
// Content queda en minusculas porque el campo de la API es asi.
type SearchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    *string `json:"site_name"`
	SiteIcon    *string `json:"site_icon"`
	Date        *string `json:"date"`
	Authority   int     `json:"authority"`
}

// SearchResponse es la respuesta de la tool.
type SearchResponse struct {
	Results     []SearchResult
	FilteredOut int
	CostUSD     float64
	Retried     bool
}

// ParseSearchResults decodifica la respuesta de la API.
func ParseSearchResults(raw []byte) ([]SearchResult, *faults.Fault) {
	var parsed struct {
		Results []SearchResult `json:"results"`
	}
	if err := json.Unmarshal(trimJSON(raw), &parsed); err != nil {
		return nil, faults.Classify(faults.ToolSearch, 200, nil, err)
	}
	return parsed.Results, nil
}

// ApplyFilter aplica min_rerank_score y declara cuántos descartó. El filtro es
// del llamador, no del servidor: por eso el efecto se informa.
func ApplyFilter(results []SearchResult, min *float64) (kept []SearchResult, filtered int) {
	if min == nil {
		return results, 0
	}
	kept = make([]SearchResult, 0, len(results))
	for _, r := range results {
		if r.RerankScore >= *min {
			kept = append(kept, r)
		} else {
			filtered++
		}
	}
	return kept, filtered
}

// trimJSON quita el prefijo de espacios en blanco que la API usa como keep-alive.
func trimJSON(raw []byte) []byte { return []byte(strings.TrimLeft(string(raw), " \t\r\n")) }
