package deepseek

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AldereteSergio/zond-mcp/internal/faults"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "sk-test", nil)
	waits := &[]time.Duration{}
	c.sleep = func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}
	return c, waits
}

// QA-030
func TestSearchSendsHeadersAndBody(t *testing.T) {
	var gotAuth, gotCT, gotUA string
	var gotBody string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"results":[{"title":"t","url":"u","rerank_score":2.4,"authority":3}]}`)
	})

	raw, fault := c.Search(context.Background(), []byte(`{"query":"x"}`))
	if fault != nil {
		t.Fatal(fault)
	}
	if len(raw) == 0 {
		t.Fatal("cuerpo vacío")
	}
	if gotAuth != "Bearer sk-test" || gotCT != "application/json" || !strings.HasPrefix(gotUA, "zond-mcp/") {
		t.Fatalf("headers: auth=%q ct=%q ua=%q", gotAuth, gotCT, gotUA)
	}
	if gotBody != `{"query":"x"}` {
		t.Fatalf("body=%q", gotBody)
	}
}

// QA-041: 200 con error adentro, y sin reintento.
func TestOCRStatus200WithErrorBody(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		io.WriteString(w, "\n\n"+`{"error":{"message":"OCR did not finish in time","type":"gateway_timeout_error","param":null,"code":"gateway_timeout_error"}}`)
	})
	body := OCRBody{ContentType: "multipart/form-data; boundary=x", Build: func() (io.Reader, error) {
		return strings.NewReader("--x--\r\n"), nil
	}}
	_, fault := c.OCR(context.Background(), body)
	if fault == nil || fault.Category != faults.CatTimeout {
		t.Fatalf("esperaba timeout, obtuve %+v", fault)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("un timeout de OCR no se reintenta: %d llamadas", calls)
	}
}

// QA-052
func TestNoRetryOn402(t *testing.T) {
	var calls int32
	c, waits := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(402)
		io.WriteString(w, `{"error":{"message":"insufficient balance","code":"insufficient_balance"}}`)
	})
	_, fault := c.Search(context.Background(), []byte(`{"query":"x"}`))
	if fault == nil || fault.Category != faults.CatPayment {
		t.Fatalf("esperaba payment, obtuve %+v", fault)
	}
	if calls != 1 || len(*waits) != 0 {
		t.Fatalf("402 no se reintenta: calls=%d waits=%v", calls, *waits)
	}
}

// QA-050: 429 con espera fija de 2 s y un solo reintento.
func TestRetryOn429(t *testing.T) {
	var calls int32
	c, waits := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"qps","code":"rate_limit_error"}}`)
			return
		}
		io.WriteString(w, `{"results":[]}`)
	})
	raw, fault := c.Search(context.Background(), []byte(`{"query":"x"}`))
	if fault != nil {
		t.Fatal(fault)
	}
	if len(raw) == 0 || calls != 2 {
		t.Fatalf("esperaba 2 llamadas, hubo %d", calls)
	}
	if len(*waits) != 1 || (*waits)[0] != retry429 {
		t.Fatalf("espera: %v", *waits)
	}
}

// QA-051: 5xx reintenta una vez con espera de 500 ms.
func TestRetryOn5xx(t *testing.T) {
	var calls int32
	c, waits := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, `{"results":[]}`)
	})
	if _, fault := c.Search(context.Background(), []byte(`{"query":"x"}`)); fault != nil {
		t.Fatal(fault)
	}
	if len(*waits) != 1 || (*waits)[0] != retry5xx {
		t.Fatalf("espera: %v", *waits)
	}
	// Techo de dos intentos: con dos 500 no hay tercero.
	atomic.StoreInt32(&calls, 0)
	c2, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(500)
	})
	c2.Search(context.Background(), []byte(`{"query":"x"}`))
	if calls != 2 {
		t.Fatalf("techo de dos intentos, hubo %d", calls)
	}
}

// QA-053b: un 429 de OCR no se reintenta.
func TestOCRNoRetryOn429(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(429)
	})
	body := OCRBody{ContentType: "multipart/form-data; boundary=x", Build: func() (io.Reader, error) {
		return strings.NewReader("--x--\r\n"), nil
	}}
	_, fault := c.OCR(context.Background(), body)
	if fault == nil || fault.Category != faults.CatRateLimit {
		t.Fatalf("esperaba rate_limit, obtuve %+v", fault)
	}
	if calls != 1 {
		t.Fatalf("429 de OCR no se reintenta: %d", calls)
	}
}

// QA-054: si el contexto se cancela durante la espera, no hay segundo intento.
func TestCancelDuringWaitStopsRetry(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(503)
	})
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, fault := c.Search(ctx, []byte(`{"query":"x"}`))
	if fault == nil || fault.Category != faults.CatCancelled {
		t.Fatalf("esperaba cancelled, obtuve %+v", fault)
	}
	if calls != 1 {
		t.Fatalf("no debería haber segundo intento: %d", calls)
	}
}

// QA-032, QA-035: multipart con dos campos y cuerpo con newlines de keep-alive.
func TestOCRMultipartAndKeepAlive(t *testing.T) {
	var fields map[string]string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		fields = map[string]string{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(part)
			fields[part.FormName()] = string(b)
		}
		io.WriteString(w, "\n\n\n"+`{"id":"ocr-1","object":"ocr.result","page_count":1,"pages":[{"index":0,"text":"hola","finish_reason":"stop"}]}`)
	})

	body := OCRBody{ContentType: "multipart/form-data; boundary=x", Build: func() (io.Reader, error) {
		return strings.NewReader("--x--\r\n"), nil
	}}
	raw, fault := c.OCR(context.Background(), body)
	if fault != nil {
		t.Fatal(fault)
	}
	if !strings.Contains(string(raw), "ocr-1") {
		t.Fatalf("cuerpo: %s", raw)
	}
	if _, ok := fields["purpose"]; ok {
		t.Fatal("este test no es del contenido del multipart")
	}
}

// QA-034: el timeout de OCR es mayor al de la API.
func TestTimeouts(t *testing.T) {
	if OCRTimeout <= 180*time.Second {
		t.Fatalf("OCRTimeout=%v debe superar los 180 s de la API", OCRTimeout)
	}
	if JSONTimeout >= OCRTimeout {
		t.Fatalf("JSONTimeout=%v debería ser menor que OCRTimeout", JSONTimeout)
	}
}

// QA-081/QA-082: el saldo se lee y el cero se distingue.
func TestCheckBalance(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/user/balance") {
			t.Fatalf("pedido inesperado: %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.50"}]}`)
	})
	b, fault := c.CheckBalance(context.Background())
	if fault != nil {
		t.Fatal(fault)
	}
	if !b.Available || b.Total != "12.50" || b.Currency != "USD" {
		t.Fatalf("saldo: %+v", b)
	}
}

func TestConfigured(t *testing.T) {
	if (&Client{}).Configured() {
		t.Fatal("sin clave no está configurado")
	}
	if !New("http://x", "k", nil).Configured() {
		t.Fatal("con clave sí")
	}
}

// Un reintento de OCR tiene que reenviar el archivo completo: si el handle quedó
// al final, el segundo multipart saldría vacío y la API diría "documento vacío".
func TestOCRRetryResendsFullBody(t *testing.T) {
	var calls int32
	var sizes []int
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		sizes = append(sizes, int(n))
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, `{"id":"ocr-2","page_count":1,"pages":[{"index":0,"text":"ok","finish_reason":"stop"}]}`)
	})
	body := OCRBody{ContentType: "multipart/form-data; boundary=x", Build: func() (io.Reader, error) {
		return strings.NewReader(strings.Repeat("x", 500)), nil
	}}
	if _, fault := c.OCR(context.Background(), body); fault != nil {
		t.Fatal(fault)
	}
	if calls != 2 {
		t.Fatalf("esperaba dos intentos, hubo %d", calls)
	}
	if sizes[0] != sizes[1] || sizes[0] != 500 {
		t.Fatalf("el reintento cambió el cuerpo: %v", sizes)
	}
}
