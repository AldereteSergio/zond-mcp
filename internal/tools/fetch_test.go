package tools

import (
	"strings"
	"testing"
)

// QA-015
func TestURLValidation(t *testing.T) {
	bad := []string{
		"ftp://x.com/a",
		"sin-esquema",
		"https://",
		"https://user:pass@x.com/a",
		"https://x.com/" + strings.Repeat("a", 8300),
	}
	for _, u := range bad {
		if f := (FetchParams{URL: u}).Validate(); f == nil {
			t.Fatalf("%q debería fallar", u)
		}
	}
	good := []string{"https://x.com", "http://x.com/a?b=1", "https://x.com/" + strings.Repeat("a", 8000)}
	for _, u := range good {
		if f := (FetchParams{URL: u}).Validate(); f != nil {
			t.Fatalf("%q debería pasar: %v", u, f)
		}
	}
}

// QA-060, QA-063, QA-065b, QA-065c
func TestApplyWindow(t *testing.T) {
	content := strings.Repeat("a", 1000)
	page := FetchPage{Title: "t", Content: content}

	r := ApplyWindow(page, FetchParams{})
	if !r.Truncated || r.TotalChars != 1000 || r.NextOffset != 50000 {
		// max por defecto mayor al contenido: no debería truncar
		if r.Truncated {
			t.Fatalf("no debería truncar con el default: %+v", r)
		}
	}

	r = ApplyWindow(page, FetchParams{MaxChars: 100})
	if !r.Truncated || r.TotalChars != 1000 || r.NextOffset != 100 || len(r.Content) != 100 {
		t.Fatalf("ventana mal: %+v", r)
	}

	prev := 1000
	r = ApplyWindow(page, FetchParams{MaxChars: 100, Offset: 100, ExpectedTotalChars: &prev})
	if r.Changed {
		t.Fatal("el total coincide, no debería marcar cambio")
	}
	other := 900
	r = ApplyWindow(page, FetchParams{MaxChars: 100, Offset: 100, ExpectedTotalChars: &other})
	if !r.Changed {
		t.Fatal("el total no coincide, debería marcar cambio")
	}

	if f := (FetchParams{URL: "https://x.com", MaxChars: 500000}).Validate(); f == nil {
		t.Fatal("max_chars sobre el techo debería fallar")
	}
}
