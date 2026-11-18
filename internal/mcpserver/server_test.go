package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldereteSergio/zond-mcp/internal/deepseek"
)

func connect(t *testing.T, h http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	client := deepseek.New(srv.URL, "sk-test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := Build(&mcp.Implementation{Name: "zond-mcp", Version: "test"},
		Deps{Client: client, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	go server.Run(ctx, st)

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil).
		Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// QA-070, QA-071, QA-077
func TestToolsList(t *testing.T) {
	session := connect(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	})
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 3 {
		t.Fatalf("esperaba 3 tools, hay %d", len(res.Tools))
	}
	names := map[string]string{}
	for _, tool := range res.Tools {
		names[tool.Name] = tool.Description
	}
	for _, want := range []string{"search", "fetch", "ocr"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("falta la tool %q", want)
		}
	}
	if !strings.Contains(names["search"], "need_content") {
		t.Error("la descripción servida no coincide con la constante")
	}
	if session.InitializeResult() == nil || session.InitializeResult().Instructions == "" {
		t.Fatal("el initialize debería traer instructions")
	}
}

// QA-075: un fallo sale como isError con texto.
func TestErrorResultShape(t *testing.T) {
	session := connect(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		io.WriteString(w, `{"error":{"message":"insufficient balance","code":"insufficient_balance"}}`)
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("debería ser isError")
	}
	if len(res.Content) == 0 {
		t.Fatal("sin contenido de error")
	}
	if res.StructuredContent != nil {
		t.Fatal("la v1 no emite structuredContent")
	}
}

// QA-042: lista vacía no es error.
func TestEmptyResultsIsSuccess(t *testing.T) {
	session := connect(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[]}`)
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("una lista vacía no es error")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("contenido inesperado: %T", res.Content[0])
	}
	if !strings.Contains(tc.Text, "No hay resultados") {
		t.Fatalf("texto: %s", tc.Text)
	}
	// La ventana de error de QA-077: sin structuredContent.
	if res.StructuredContent != nil {
		t.Fatalf("structuredContent no esperado: %v", res.StructuredContent)
	}
}

// QA-010 por la superficie: la validación local no toca la red.
func TestLocalValidationDoesNotHitAPI(t *testing.T) {
	var calls int32
	session := connect(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"results":[]}`)
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "   "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("debería ser error")
	}
	if calls != 0 {
		t.Fatalf("no debería haber llamado a la API: %d", calls)
	}
}

// QA-089: sin clave, el servidor responde con configuración.
func TestMissingKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	client := deepseek.New(srv.URL, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := Build(&mcp.Implementation{Name: "zond-mcp", Version: "test"},
		Deps{Client: client, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	st, ct := mcp.NewInMemoryTransports()
	go server.Run(context.Background(), st)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("sin clave debería fallar")
	}
	tc := res.Content[0].(*mcp.TextContent)
	if !strings.Contains(tc.Text, "DEEPSEEK_API_KEY") {
		t.Fatalf("el texto debe nombrar la variable: %s", tc.Text)
	}
}

var _ = json.Marshal
