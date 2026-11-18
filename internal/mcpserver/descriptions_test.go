package mcpserver

import (
	"strings"
	"testing"
)

// QA-072
func TestSearchDescription(t *testing.T) {
	required := []string{
		"site:", "-site:", "lang:", "after:", "before:", "frase exacta",
		"1 a 50", "need_content", "1.5", "authority",
		"count", "min_rerank_score",
	}
	for _, want := range required {
		if !strings.Contains(descSearch, want) {
			t.Errorf("la descripción de search no menciona %q", want)
		}
	}
	if !strings.Contains(descSearch, "50") || !strings.Contains(descSearch, "ruido") {
		t.Error("la descripción de search debe advertir que count 50 es contraproducente")
	}
}

// QA-073
func TestFetchDescription(t *testing.T) {
	required := []string{
		"offset", "max_chars", "ventana local", "se factura", "truncated", "next_offset",
	}
	for _, want := range required {
		if !strings.Contains(descFetch, want) {
			t.Errorf("la descripción de fetch no menciona %q", want)
		}
	}
}

// QA-074
func TestOCRDescription(t *testing.T) {
	required := []string{
		"ruta", "LOCAL", "text_recognition", "rich_layout_recognition",
		"cancelar", "se factura", "100 MiB", "1000",
	}
	for _, want := range required {
		if !strings.Contains(descOCR, want) {
			t.Errorf("la descripción de ocr no menciona %q", want)
		}
	}
	if !strings.Contains(descOCR, "NO cancela") {
		t.Error("la descripción de ocr debe decir que cancelar no cancela el cobro")
	}
}

// QA-076b
func TestInstructionsBudget(t *testing.T) {
	if strings.TrimSpace(instructions) == "" {
		t.Fatal("instructions vacías")
	}
	if len(instructions) > MaxInstructionsChars {
		t.Fatalf("instructions mide %d y el tope es %d", len(instructions), MaxInstructionsChars)
	}
	if !strings.Contains(instructions, "zond-mcp") {
		t.Error("las instructions deberían nombrar el servidor")
	}
}
