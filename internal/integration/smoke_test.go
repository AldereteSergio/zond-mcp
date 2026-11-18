//go:build integration

// Package integration contiene las pruebas que sí pegan contra la API real.
//
// No corren por defecto: cuestan dinero y necesitan una clave con saldo. Se
// ejecutan a mano con `DEEPSEEK_API_KEY=... go test -tags integration ./...`.
// La función de estas pruebas no es garantizar el contrato ajeno sino fallar
// ruidosamente cuando el contrato cambia.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldereteSergio/zond-mcp/internal/deepseek"
)

func key(t *testing.T) string {
	t.Helper()
	k := os.Getenv("DEEPSEEK_API_KEY")
	if strings.TrimSpace(k) == "" {
		t.Skip("sin DEEPSEEK_API_KEY: las pruebas de integración no corren")
	}
	return k
}

func baseURL() string {
	if v := os.Getenv("DEEPSEEK_BASE_URL"); v != "" {
		return v
	}
	return "https://api.deepseek.com"
}

// QA-081: el saldo, que no factura como request de tools.
func TestBalance(t *testing.T) {
	c := deepseek.New(baseURL(), key(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, fault := c.CheckBalance(ctx)
	if fault != nil {
		t.Fatalf("saldo: %v", fault)
	}
	t.Logf("saldo disponible: %s %s (available=%v)", b.Total, b.Currency, b.Available)
	if !b.Available {
		t.Fatalf("la cuenta no está disponible: %+v", b)
	}
}

// startServer levanta el binario real por stdio y devuelve una sesión MCP.
func startServer(t *testing.T) *mcp.ClientSession {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./cmd/zond-mcp")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY="+key(t), "DEEPSEEK_LOG_LEVEL=error")

	client := mcp.NewClient(&mcp.Implementation{Name: "integration", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("no se pudo conectar al servidor: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callText(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: error de protocolo: %v", tool, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("%s: sin contenido", tool)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s: contenido inesperado %T", tool, res.Content[0])
	}
	return text.Text, res.IsError
}

// QA-084, QA-085
func TestSearchReal(t *testing.T) {
	s := startServer(t)
	text, isErr := callText(t, s, "search", map[string]any{"query": "DeepSeek API tools search", "count": 2})
	if isErr {
		t.Fatalf("search falló: %s", text)
	}
	if !strings.Contains(text, "costo estimado USD 0.00075") {
		t.Errorf("falta la línea de costo: %s", text)
	}
	if !strings.Contains(text, "1.") {
		t.Errorf("no hay resultados: %s", text)
	}
	t.Logf("respuesta:\n%s", truncate(text, 1200))
}

// QA-086
func TestFetchReal(t *testing.T) {
	s := startServer(t)
	text, isErr := callText(t, s, "fetch", map[string]any{"url": "https://api-docs.deepseek.com/guides/search"})
	if isErr {
		t.Fatalf("fetch falló: %s", text)
	}
	if !strings.Contains(text, "Search & Fetch") && !strings.Contains(text, "Search") {
		t.Errorf("no parece el contenido esperado: %s", truncate(text, 400))
	}
	t.Logf("respuesta:\n%s", truncate(text, 800))
}

// QA-087: OCR contra un PDF de una página generado en el test.
func TestOCRReal(t *testing.T) {
	s := startServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "una-pagina.pdf")
	if err := os.WriteFile(path, buildPDF("Hola desde zond-mcp"), 0o600); err != nil {
		t.Fatal(err)
	}

	text, isErr := callText(t, s, "ocr", map[string]any{"path": path, "purpose": "rich_layout_recognition"})
	if isErr {
		t.Fatalf("ocr falló: %s", text)
	}
	if !strings.Contains(text, "documento de 1 páginas") {
		t.Errorf("page_count inesperado: %s", truncate(text, 400))
	}
	t.Logf("respuesta:\n%s", truncate(text, 800))
}

// QA-088: con una clave inválida el servidor responde y sigue vivo.
func TestBadKeyKeepsServerAlive(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./cmd/zond-mcp")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=sk-invalida", "DEEPSEEK_LOG_LEVEL=error")
	client := mcp.NewClient(&mcp.Implementation{Name: "integration", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	text, isErr := callText(t, session, "search", map[string]any{"query": "x"})
	if !isErr || !strings.Contains(strings.ToLower(text), "clave") {
		t.Fatalf("esperaba error de clave: %s", text)
	}
	// Y sigue respondiendo.
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("el servidor debería seguir vivo: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// buildPDF arma un PDF de una página con texto, con la tabla xref calculada.
func buildPDF(text string) []byte {
	var b bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	offsets := make([]int, 6)

	w("%%PDF-1.4\n")
	offsets[1] = b.Len()
	w("1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n")
	offsets[2] = b.Len()
	w("2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n")
	offsets[3] = b.Len()
	w("3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 200]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n")

	stream := fmt.Sprintf("BT /F1 18 Tf 20 100 Td (%s) Tj ET", text)
	offsets[4] = b.Len()
	w("4 0 obj<</Length %d>>stream\n%s\nendstream endobj\n", len(stream), stream)

	offsets[5] = b.Len()
	w("5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\n")

	xref := b.Len()
	w("xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		w("%010d 00000 n \n", offsets[i])
	}
	w("trailer<</Size 6/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}
