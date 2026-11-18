package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldereteSergio/zond-mcp/internal/deepseek"
	"github.com/AldereteSergio/zond-mcp/internal/faults"
	"github.com/AldereteSergio/zond-mcp/internal/tools"
)

// Deps son las dependencias del servidor.
type Deps struct {
	Client *deepseek.Client
	Log    *slog.Logger
}

// Build registra las tools y devuelve el servidor listo para correr.
func Build(impl *mcp.Implementation, deps Deps) *mcp.Server {
	s := &state{deps: deps}
	server := mcp.NewServer(impl, &mcp.ServerOptions{Instructions: instructions})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Title:       "Búsqueda web estructurada",
		Description: descSearch,
	}, s.search)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch",
		Title:       "Lectura de una página en Markdown",
		Description: descFetch,
	}, s.fetch)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ocr",
		Title:       "Reconocimiento de texto en documentos",
		Description: descOCR,
	}, s.ocr)

	return server
}

type state struct{ deps Deps }

// --- search ---------------------------------------------------------------

type searchIn struct {
	Query          string   `json:"query" jsonschema:"Consulta de búsqueda, 1 a 128 caracteres. Acepta operadores como site:, -site:, -termino, \"frase\", lang:es, after:2025, before:2026."`
	Count          *int     `json:"count,omitempty" jsonschema:"Cantidad de resultados, 1 a 50. Si se omite, la API devuelve 10. Para mas cobertura conviene varias queries distintas y no un count alto."`
	NeedContent    *bool    `json:"need_content,omitempty" jsonschema:"Si es true, cada resultado trae el texto de la pagina en content. Por defecto false."`
	OriQuestion    *string  `json:"ori_question,omitempty" jsonschema:"Pregunta original del usuario, 1 a 128 caracteres. Mejora la comprension de la query."`
	Localization   *string  `json:"localization,omitempty" jsonschema:"Direccion IP para localizar resultados. No es un codigo de pais."`
	MinRerankScore *float64 `json:"min_rerank_score,omitempty" jsonschema:"Filtro local de 0 a 3; descarta resultados por debajo del umbral e informa cuantos filtro. No es un parametro de la API."`
}

func (s *state) search(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
	if f := s.requireKey(); f != nil {
		return nil, nil, f
	}
	params := tools.SearchParams{
		Query:          in.Query,
		Count:          in.Count,
		NeedContent:    in.NeedContent,
		OriQuestion:    in.OriQuestion,
		Localization:   in.Localization,
		MinRerankScore: in.MinRerankScore,
	}
	if f := params.Validate(); f != nil {
		return nil, nil, f
	}
	payload, f := params.Payload()
	if f != nil {
		return nil, nil, f
	}
	raw, f := s.deps.Client.Search(ctx, payload)
	if f != nil {
		return nil, nil, f
	}
	results, f := tools.ParseSearchResults(raw)
	if f != nil {
		return nil, nil, f
	}
	kept, filtered := tools.ApplyFilter(results, params.MinRerankScore)

	resp := tools.SearchResponse{Results: kept, FilteredOut: filtered, CostUSD: deepseek.PricePerRequestUSD}
	return text(renderSearch(resp)), nil, nil
}

func renderSearch(r tools.SearchResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[zond-mcp] search — %d resultados", len(r.Results))
	if r.FilteredOut > 0 {
		fmt.Fprintf(&b, " (%d filtrados por min_rerank_score)", r.FilteredOut)
	}
	fmt.Fprintf(&b, " · costo estimado USD %.5f\n", r.CostUSD)

	if len(r.Results) == 0 {
		b.WriteString("\nNo hay resultados para esta consulta. Probá con otros términos, o quitá operadores que estén acotando de más.\n")
		return b.String()
	}

	for i, item := range r.Results {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, item.Title, item.URL)
		fmt.Fprintf(&b, "   rerank %.2f · authority %d", item.RerankScore, item.Authority)
		if item.SiteName != nil && *item.SiteName != "" {
			fmt.Fprintf(&b, " · %s", *item.SiteName)
		}
		if item.Date != nil && *item.Date != "" {
			fmt.Fprintf(&b, " · %s", *item.Date)
		}
		b.WriteString("\n")
		if item.Summary != "" {
			fmt.Fprintf(&b, "   %s\n", strings.TrimSpace(item.Summary))
		} else if item.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", strings.TrimSpace(item.Snippet))
		}
		if item.Content != "" {
			fmt.Fprintf(&b, "   --- contenido ---\n   %s\n", strings.TrimSpace(item.Content))
		}
	}
	return b.String()
}

// --- fetch ----------------------------------------------------------------

type fetchIn struct {
	URL                string `json:"url" jsonschema:"URL HTTP o HTTPS con host, sin usuario ni contrasena, hasta 8192 caracteres."`
	MaxChars           *int   `json:"max_chars,omitempty" jsonschema:"Ventana local sobre la respuesta: cuantos caracteres devolver. No es un parametro de la API."`
	Offset             *int   `json:"offset,omitempty" jsonschema:"Ventana local: desde que caracter seguir. Usa el next_offset que devolvio la llamada anterior."`
	ExpectedTotalChars *int   `json:"expected_total_chars,omitempty" jsonschema:"Opcional: el total_chars que devolvio la llamada anterior. Sirve para detectar que la pagina cambio entre ventanas."`
}

func (s *state) fetch(ctx context.Context, _ *mcp.CallToolRequest, in fetchIn) (*mcp.CallToolResult, any, error) {
	if f := s.requireKey(); f != nil {
		return nil, nil, f
	}
	params := tools.FetchParams{URL: in.URL, ExpectedTotalChars: in.ExpectedTotalChars}
	if in.MaxChars != nil {
		params.MaxChars = *in.MaxChars
	}
	if in.Offset != nil {
		params.Offset = *in.Offset
	}
	if f := params.Validate(); f != nil {
		return nil, nil, f
	}
	payload, f := params.Payload()
	if f != nil {
		return nil, nil, f
	}
	raw, f := s.deps.Client.Fetch(ctx, payload)
	if f != nil {
		return nil, nil, f
	}
	page, f := tools.ParseFetchPage(raw)
	if f != nil {
		return nil, nil, f
	}
	return text(renderFetch(tools.ApplyWindow(page, params))), nil, nil
}

func renderFetch(r tools.FetchResponse) string {
	var b strings.Builder
	title := r.Title
	if title == "" {
		title = "(sin título)"
	}
	fmt.Fprintf(&b, "[zond-mcp] fetch — %s · ventana desde %d, total %d caracteres · costo estimado USD %.5f\n",
		title, r.Offset, r.TotalChars, r.CostUSD)

	if r.Changed {
		b.WriteString("\n--- [servidor] la página cambió entre la ventana anterior y esta: el contenido puede estar desalineado ---\n")
	}
	if r.Truncated {
		fmt.Fprintf(&b, "\n--- [servidor] queda contenido: pedí de nuevo con offset=%d (esa llamada se factura otra vez) ---\n", r.NextOffset)
	} else if r.Offset > 0 {
		b.WriteString("\n--- [servidor] fin del contenido ---\n")
	}

	b.WriteString("\n")
	b.WriteString(r.Content)
	if !strings.HasSuffix(r.Content, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// --- ocr ------------------------------------------------------------------

type ocrIn struct {
	Path     string `json:"path" jsonschema:"Ruta de un archivo LOCAL (no una URL). El servidor lee ese archivo del disco."`
	Purpose  string `json:"purpose" jsonschema:"Obligatorio: text_recognition (texto directo en Word/PowerPoint sin imagenes, resto pagina por pagina) o rich_layout_recognition (todas las paginas respetando el layout)."`
	FromPage *int   `json:"from_page,omitempty" jsonschema:"Ventana local de paginas: desde cual empezar. Usa el next_page de la llamada anterior."`
	MaxPages *int   `json:"max_pages,omitempty" jsonschema:"Ventana local de paginas: cuantas devolver. Por defecto 50, maximo 200."`
}

func (s *state) ocr(ctx context.Context, _ *mcp.CallToolRequest, in ocrIn) (*mcp.CallToolResult, any, error) {
	if f := s.requireKey(); f != nil {
		return nil, nil, f
	}
	params := tools.OCRParams{Path: in.Path, Purpose: in.Purpose}
	if in.FromPage != nil {
		params.FromPage = *in.FromPage
	}
	if in.MaxPages != nil {
		params.MaxPages = *in.MaxPages
	}
	if f := params.Validate(); f != nil {
		return nil, nil, f
	}
	file, _, f := tools.OpenFile(params.Path)
	if f != nil {
		return nil, nil, f
	}
	defer file.Close()

	body := tools.BuildMultipart(file, filepath.Base(params.Path), params.Purpose)
	raw, f := s.deps.Client.OCR(ctx, body)
	if f != nil {
		return nil, nil, f
	}
	result, f := tools.ParseOCR(raw)
	if f != nil {
		return nil, nil, f
	}
	return text(renderOCR(tools.ApplyPageWindow(result, params))), nil, nil
}

func renderOCR(r tools.OCRResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[zond-mcp] ocr — documento de %d páginas · ventana de %d · costo estimado USD %.6f\n",
		r.PageCount, len(r.Pages), r.CostUSD)
	if len(r.Pages) == 0 {
		fmt.Fprintf(&b, "\nLa ventana quedó vacía: el documento tiene %d páginas y pediste desde la %d. Usá un from_page menor.\n", r.PageCount, r.FromPage)
		return b.String()
	}
	if r.Truncated {
		fmt.Fprintf(&b, "\n--- [servidor] queda resto: pedí de nuevo con from_page=%d ---\n", r.NextPage)
	}
	if r.CutWarning {
		b.WriteString("\n--- [servidor] alguna página se recortó por tamaño: se indica en su bloque ---\n")
	}

	for _, p := range r.Pages {
		fmt.Fprintf(&b, "\n=== página %d (finish_reason: %s)", p.Index, p.FinishReason)
		if p.PageTruncated {
			fmt.Fprintf(&b, " — recortada por el servidor: %d de %d caracteres", p.PageCharsKept, p.PageTotalChars)
		}
		b.WriteString(" ===\n")
		b.WriteString(p.Text)
		if !strings.HasSuffix(p.Text, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// --- helpers --------------------------------------------------------------

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// requireKey convierte la falta de clave en un error de configuración en la
// primera llamada, en vez de matar el proceso al arrancar.
func (s *state) requireKey() *faults.Fault {
	if s.deps.Client == nil || !s.deps.Client.Configured() {
		return faults.NotConfigured()
	}
	return nil
}

// MaxInstructionsChars es el tope del texto de instructions (QA-076b).
const MaxInstructionsChars = 2000
