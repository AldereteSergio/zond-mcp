package tools

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/AldereteSergio/zond-mcp/internal/faults"
	"github.com/AldereteSergio/zond-mcp/internal/window"
)

// URLMaxLen es el límite documentado en api-fetch.txt.
const URLMaxLen = 8192

// FetchParams es la entrada de la tool fetch.
type FetchParams struct {
	URL string
	// Ventana local sobre la respuesta. No son parámetros de la API.
	Offset   int
	MaxChars int
	// ExpectedTotalChars permite detectar que la página cambió entre ventanas.
	ExpectedTotalChars *int
}

// Validate revisa la URL antes de tocar la red (QA-015, QA-016).
func (p FetchParams) Validate() *faults.Fault {
	if len(p.URL) > URLMaxLen {
		return faults.InvalidInput(
			fmt.Sprintf("La URL tiene %d caracteres y el máximo es %d.", len(p.URL), URLMaxLen),
			"Acortá la URL; si trae parámetros de tracking, quitálos.")
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return faults.InvalidInput("La URL no se puede parsear.", "Revisá que esté bien escrita.")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return faults.InvalidInput(
			fmt.Sprintf("El esquema de la URL es %q.", u.Scheme),
			"Solo se aceptan http o https.")
	}
	if u.Host == "" {
		return faults.InvalidInput("La URL no tiene host.", "Incluí el dominio completo, por ejemplo https://ejemplo.com/pagina.")
	}
	if u.User != nil {
		return faults.InvalidInput("La URL incluye usuario o contraseña.",
			"Quitá las credenciales de la URL; la API las rechaza.")
	}
	if p.MaxChars < 0 || p.MaxChars > window.MaxCharsCeiling {
		return faults.InvalidInput(
			fmt.Sprintf("max_chars vale %d y el máximo es %d.", p.MaxChars, window.MaxCharsCeiling),
			fmt.Sprintf("Usá un valor entre 0 y %d; si lo omitís se usan %d.", window.MaxCharsCeiling, window.DefaultMaxChars))
	}
	if p.Offset < 0 {
		return faults.InvalidInput("offset no puede ser negativo.", "Usá el next_offset que devolvió la llamada anterior.")
	}
	return nil
}

// Payload arma el cuerpo JSON del pedido a la API.
func (p FetchParams) Payload() ([]byte, *faults.Fault) {
	raw, err := json.Marshal(map[string]string{"url": p.URL})
	if err != nil {
		return nil, faults.Classify(faults.ToolFetch, 0, nil, err)
	}
	return raw, nil
}

// FetchPage es la respuesta de la API, sin renombrar.
type FetchPage struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// FetchResponse es la respuesta de la tool, con la ventana ya aplicada.
type FetchResponse struct {
	Title      string
	Content    string
	Truncated  bool
	TotalChars int
	NextOffset int
	Offset     int
	// Changed informa que la página no es la misma que en la ventana anterior.
	Changed bool
	CostUSD float64
}

// ApplyWindow recorta el contenido y detecta si la página cambió entre llamadas.
func ApplyWindow(page FetchPage, p FetchParams) FetchResponse {
	maxChars := p.MaxChars
	if maxChars == 0 {
		maxChars = window.DefaultMaxChars
	}
	if maxChars > window.MaxCharsCeiling {
		maxChars = window.MaxCharsCeiling
	}

	chunk, truncated, total, next := window.Text(page.Content, maxChars, p.Offset)

	changed := false
	if p.ExpectedTotalChars != nil && *p.ExpectedTotalChars != total {
		// La página se volvió a pedir: el servidor no retiene contenido entre
		// llamadas, así que la segunda ventana puede ser de otra versión. Se
		// declara en vez de dejar que el agente concatene dos versiones.
		changed = true
	}

	return FetchResponse{
		Title:      page.Title,
		Content:    chunk,
		Truncated:  truncated,
		TotalChars: total,
		NextOffset: next,
		Offset:     p.Offset,
		Changed:    changed,
		CostUSD:    costPerRequest(),
	}
}

// ParseFetchPage decodifica la respuesta de la API.
func ParseFetchPage(raw []byte) (FetchPage, *faults.Fault) {
	var page FetchPage
	if err := json.Unmarshal(trimJSON(raw), &page); err != nil {
		return FetchPage{}, faults.Classify(faults.ToolFetch, 200, nil, err)
	}
	return page, nil
}
