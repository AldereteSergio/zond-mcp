package tools

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AldereteSergio/zond-mcp/internal/deepseek"
)

// El reintento de OCR tiene que reenviar el archivo completo usando el armador
// REAL. Un test con un Build propio no ve el bug del handle al final: este sí,
// porque pasa por BuildMultipart y por el cliente de verdad.
func TestOCRRetryResendsFullFileWithRealBuilder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	payload := "%PDF-1.4 " + strings.Repeat("contenido ", 100)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls int32
	bodies := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		var file string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if part.FormName() == "file" {
				b, _ := io.ReadAll(part)
				file = string(b)
			}
		}
		bodies = append(bodies, file)
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(500) // transient: se reintenta
			return
		}
		io.WriteString(w, `{"id":"ocr-x","page_count":1,"pages":[{"index":0,"text":"ok","finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	client := deepseek.New(srv.URL, "sk-test", nil)

	file, _, fault := OpenFile(path)
	if fault != nil {
		t.Fatal(fault)
	}
	defer file.Close()

	if _, fault := client.OCR(context.Background(), BuildMultipart(file, "doc.pdf", PurposeText)); fault != nil {
		t.Fatal(fault)
	}
	if len(bodies) != 2 {
		t.Fatalf("esperaba dos intentos, hubo %d", len(bodies))
	}
	if bodies[0] != payload {
		t.Fatalf("primer intento incompleto: %d bytes de %d", len(bodies[0]), len(payload))
	}
	if bodies[1] != payload {
		t.Fatalf("el reintento reenvió %d bytes de %d: el handle quedó al final", len(bodies[1]), len(payload))
	}
}
