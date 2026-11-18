package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func ptrInt(v int) *int       { return &v }
func ptrBool(v bool) *bool    { return &v }
func ptrStr(v string) *string { return &v }
func ptrF(v float64) *float64 { return &v }

// QA-010, QA-011
func TestQueryValidation(t *testing.T) {
	bad := []string{"", "   ", strings.Repeat("a", 129)}
	for _, q := range bad {
		if f := (SearchParams{Query: q}).Validate(); f == nil {
			t.Fatalf("%q debería ser inválida", q)
		}
	}
	good := []string{"a", strings.Repeat("a", 128)}
	for _, q := range good {
		if f := (SearchParams{Query: q}).Validate(); f != nil {
			t.Fatalf("%q debería ser válida: %v", q, f)
		}
	}
}

// QA-013c: se cuenta en runas, no en bytes.
func TestQueryCountsRunes(t *testing.T) {
	q := strings.Repeat("漢", 128) // 384 bytes
	if f := (SearchParams{Query: q}).Validate(); f != nil {
		t.Fatalf("128 caracteres CJK deberían ser válidos: %v", f)
	}
}

// QA-012
func TestCountRange(t *testing.T) {
	for _, c := range []int{-1, 0, 51} {
		if f := (SearchParams{Query: "x", Count: ptrInt(c)}).Validate(); f == nil {
			t.Fatalf("count=%d debería fallar", c)
		}
	}
	if f := (SearchParams{Query: "x", Count: ptrInt(50)}).Validate(); f != nil {
		t.Fatalf("count=50 es válido: %v", f)
	}
}

// QA-013b: los opcionales ausentes no viajan, y localization nunca va como null.
func TestPayloadOmitsAbsentFields(t *testing.T) {
	p := SearchParams{Query: "x"}
	raw, _ := p.Payload()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"count", "need_content", "ori_question", "localization"} {
		if _, ok := m[k]; ok {
			t.Fatalf("%q no debería viajar: %s", k, raw)
		}
	}

	withNulls := SearchParams{Query: "x", Localization: ptrStr(""), Count: ptrInt(0)}
	raw, _ = withNulls.Payload()
	m = map[string]any{}
	json.Unmarshal(raw, &m)
	if _, ok := m["localization"]; ok {
		t.Fatalf("localization vacía no debe viajar: %s", raw)
	}
	if v, ok := m["count"]; !ok || v.(float64) != 0 {
		t.Fatalf("count ausente vs cero: %s", raw)
	}
}

// QA-013d
func TestLocalizationMustBeIP(t *testing.T) {
	for _, v := range []string{"ar", "Buenos Aires", "1.2.3.4.5"} {
		if f := (SearchParams{Query: "x", Localization: ptrStr(v)}).Validate(); f == nil {
			t.Fatalf("%q no es IP y debería fallar", v)
		}
	}
	for _, v := range []string{"1.2.3.4", "::1", "2001:db8::1"} {
		if f := (SearchParams{Query: "x", Localization: ptrStr(v)}).Validate(); f != nil {
			t.Fatalf("%q es IP y debería pasar: %v", v, f)
		}
	}
}

// QA-014
func TestMinRerankValidation(t *testing.T) {
	if f := (SearchParams{Query: "x", MinRerankScore: ptrF(3.5)}).Validate(); f == nil {
		t.Fatal("3.5 fuera de rango")
	}
	if f := (SearchParams{Query: "x", MinRerankScore: ptrF(1.5)}).Validate(); f != nil {
		t.Fatalf("1.5 es válido: %v", f)
	}
}

// QA-014 + QA-042
func TestApplyFilterDeclaresCount(t *testing.T) {
	results := []SearchResult{{RerankScore: 2.4}, {RerankScore: 1.0}, {RerankScore: 1.5}}
	kept, filtered := ApplyFilter(results, ptrF(1.5))
	if len(kept) != 2 || filtered != 1 {
		t.Fatalf("kept=%d filtered=%d", len(kept), filtered)
	}
	kept, filtered = ApplyFilter(results, nil)
	if len(kept) != 3 || filtered != 0 {
		t.Fatalf("sin filtro no se descarta nada")
	}
}
