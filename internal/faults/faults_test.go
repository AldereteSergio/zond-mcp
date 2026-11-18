package faults

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func body(msg, code string) []byte {
	return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"x","param":null,"code":%q}}`, msg, code))
}

// QA-040
func TestStatusTable(t *testing.T) {
	cases := []struct {
		tool   Tool
		status int
		want   Category
		retry  bool
	}{
		{ToolSearch, 400, CatInvalidInput, false},
		{ToolSearch, 401, CatAuth, false},
		{ToolSearch, 402, CatPayment, false},
		{ToolFetch, 403, CatPolicy, false},
		{ToolFetch, 404, CatUnusable, false},
		{ToolSearch, 408, CatTransient, true},
		{ToolOCR, 413, CatInvalidInput, false},
		{ToolSearch, 429, CatRateLimit, true},
		{ToolOCR, 429, CatRateLimit, false},
		{ToolSearch, 500, CatTransient, true},
		{ToolOCR, 503, CatTransient, true},
		{ToolSearch, 504, CatTimeout, true},
	}
	for _, c := range cases {
		f := Classify(c.tool, c.status, body("x", ""), nil)
		if f.Category != c.want || f.Retryable != c.retry {
			t.Fatalf("%s/%d: got %s retry=%v, want %s retry=%v",
				c.tool, c.status, f.Category, f.Retryable, c.want, c.retry)
		}
		if f.Action == "" {
			t.Fatalf("%s/%d: sin acción", c.tool, c.status)
		}
	}
}

// QA-041: el error escondido dentro de un 200.
func TestErrorInside200(t *testing.T) {
	for _, tool := range []Tool{ToolSearch, ToolFetch, ToolOCR} {
		f := Classify(tool, 200, body("OCR did not finish in time", "gateway_timeout_error"), nil)
		if f == nil || f.Category != CatTimeout {
			t.Fatalf("%s: esperaba timeout, obtuve %+v", tool, f)
		}
		if f.Retryable {
			t.Fatalf("%s: un timeout de OCR no se reintenta", tool)
		}
	}
}

// QA-041b: 200 con error desconocido no se convierte en éxito.
func TestUnknownErrorInside200(t *testing.T) {
	f := Classify(ToolOCR, 200, body("algo nuevo del proveedor", ""), nil)
	if f == nil || f.Category != CatInternal {
		t.Fatalf("esperaba internal, obtuve %+v", f)
	}
}

// QA-042: 0 resultados no es falla.
func TestEmptyResultsIsNotError(t *testing.T) {
	if f := Classify(ToolSearch, 200, []byte(`{"results":[]}`), nil); f != nil {
		t.Fatalf("una lista vacía no es falla: %+v", f)
	}
}

// QA-043: tabla exacta de mensajes del OCR.
func TestOCRMessageTable(t *testing.T) {
	for msg, want := range map[string]Category{
		"The file is password-protected and cannot be processed. Remove the password and upload it again.": CatUnusable,
		"Failed to recognize the file: the file format is not supported":                                   CatUnusable,
		"Failed to recognize the file: the document has more than 1000 pages":                              CatInvalidInput,
		"file is too large: max 100 MiB":                                                                   CatInvalidInput,
		"OCR did not finish within 180 seconds":                                                            CatTimeout,
		"The OCR service is temporarily unavailable. Please try again later.":                              CatTransient,
	} {
		f := Classify(ToolOCR, 200, body(msg, ""), nil)
		if f == nil || f.Category != want {
			t.Fatalf("%q: got %+v, want %s", msg, f, want)
		}
	}
	// Una paráfrasis no matchea: cae en internal y conserva el texto.
	f := Classify(ToolOCR, 200, body("Failed to recognize the file: el formato no va", ""), nil)
	if f.Category != CatInternal || f.Detail == "" {
		t.Fatalf("una paráfrasis debería caer en internal con detalle: %+v", f)
	}
}

// QA-044: ni la clave ni contenido del usuario en el texto.
func TestNoSecretsInMessage(t *testing.T) {
	f := Classify(ToolSearch, 401, body("invalid key sk-abc123", "authentication_error"), nil)
	if strings.Contains(f.Error(), "sk-abc123") {
		t.Fatalf("el error filtra la clave: %s", f.Error())
	}
}

// QA-046: errores de transporte.
func TestTransportErrors(t *testing.T) {
	if f := Classify(ToolSearch, 0, nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}); f.Category != CatNetwork {
		t.Fatalf("esperaba network, obtuve %s", f.Category)
	}
	if f := Classify(ToolSearch, 0, nil, errors.New("dial tcp 1.2.3.4:443: connect: connection refused")); f.Category != CatNetwork {
		t.Fatalf("esperaba network por texto, obtuve %s", f.Category)
	}
}

// QA-047
func TestContextErrors(t *testing.T) {
	if f := Classify(ToolSearch, 0, nil, context.Canceled); f.Category != CatCancelled || f.Retryable {
		t.Fatalf("cancelada: %+v", f)
	}
	if f := Classify(ToolSearch, 0, nil, context.DeadlineExceeded); f.Category != CatTimeout {
		t.Fatalf("deadline: %+v", f)
	}
}

// QA-045: forma del texto.
func TestErrorShape(t *testing.T) {
	f := Classify(ToolSearch, 402, body("insufficient balance", "insufficient_balance"), nil)
	got := f.Error()
	if !strings.HasSuffix(got, "(payment/402)") {
		t.Fatalf("forma inesperada: %q", got)
	}
	if f.Landed() != true {
		t.Fatal("un 402 llegó a la API")
	}
}

func TestNotConfigured(t *testing.T) {
	f := NotConfigured()
	if f.Category != CatConfiguration || f.Landed() {
		t.Fatalf("%+v", f)
	}
	if !strings.Contains(f.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("el texto debe nombrar la variable: %s", f.Error())
	}
}

// Regresión: un 401 con code "invalid_request_error" es de autenticación, no de
// entrada. Mandar al usuario a "corregí el pedido" cuando el problema es la
// clave es el tipo de error que se paga con una hora de soporte.
func TestStatusBeatsGenericCode(t *testing.T) {
	f := Classify(ToolSearch, 401, body("Authentication Fails", "invalid_request_error"), nil)
	if f.Category != CatAuth {
		t.Fatalf("esperaba auth, obtuve %s", f.Category)
	}
	f = Classify(ToolSearch, 402, body("Insufficient Balance", "invalid_request_error"), nil)
	if f.Category != CatPayment {
		t.Fatalf("esperaba payment, obtuve %s", f.Category)
	}
}

// BAJ-1: un error dentro de un 200 nunca se marca reintentable, aunque su
// categoría lo sea: reintentarlo vuelve a facturar el trabajo.
func TestErrorInside200NeverRetryable(t *testing.T) {
	f := Classify(ToolOCR, 200, body("The OCR service is temporarily unavailable. Please try again later.", ""), nil)
	if f.Category != CatTransient {
		t.Fatalf("categoría: %s", f.Category)
	}
	if f.Retryable {
		t.Fatal("un error dentro de un 200 no puede reintentarse solo")
	}
}

// BAJ-2: la tabla de mensajes es de OCR; un mensaje de OCR en fetch no aplica.
func TestOCRMessageTableOnlyForOCR(t *testing.T) {
	msg := "The file is password-protected and cannot be processed. Remove the password and upload it again."
	if f := Classify(ToolFetch, 400, body(msg, ""), nil); f.Category == CatUnusable && strings.Contains(f.Action, "contraseña") {
		t.Fatalf("no debería usar la acción de OCR en fetch: %+v", f)
	}
}
