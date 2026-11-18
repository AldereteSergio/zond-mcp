package window

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// QA-060
func TestTextBoundaries(t *testing.T) {
	s := strings.Repeat("a", 10)
	cases := []struct {
		max, offset int
		wantLen     int
		wantTrunc   bool
	}{
		{10, 0, 10, false},
		{11, 0, 10, false},
		{9, 0, 9, true},
		{1, 10, 0, false},
	}
	for _, c := range cases {
		chunk, trunc, total, _ := Text(s, c.max, c.offset)
		if utf8.RuneCountInString(chunk) != c.wantLen || trunc != c.wantTrunc || total != 10 {
			t.Fatalf("max=%d offset=%d: got len=%d trunc=%v total=%d",
				c.max, c.offset, utf8.RuneCountInString(chunk), trunc, total)
		}
	}
}

// QA-061: nunca parte un carácter multibyte.
func TestTextNeverSplitsRunes(t *testing.T) {
	s := strings.Repeat("é", 5) + strings.Repeat("漢", 5)
	for max := 1; max <= 12; max++ {
		chunk, _, _, next := Text(s, max, 0)
		if !utf8.ValidString(chunk) {
			t.Fatalf("max=%d: trozo inválido", max)
		}
		if next > 0 && !utf8.ValidString(string([]rune(s)[:next])) {
			t.Fatalf("max=%d: next=%d cae en medio de una runa", max, next)
		}
	}
}

// QA-062
func TestTextOffsetPastEnd(t *testing.T) {
	chunk, trunc, total, next := Text("abc", 10, 99)
	if chunk != "" || trunc || total != 3 || next != -1 {
		t.Fatalf("got %q %v %d %d", chunk, trunc, total, next)
	}
}

// QA-063: total es el del contenido completo, no el de la ventana.
func TestTextTotalIsFullLength(t *testing.T) {
	_, trunc, total, next := Text(strings.Repeat("x", 1000), 100, 0)
	if !trunc || total != 1000 || next != 100 {
		t.Fatalf("got trunc=%v total=%d next=%d", trunc, total, next)
	}
}

// QA-064: índices originales del documento.
func TestPagesKeepOriginalIndex(t *testing.T) {
	var pages []Page
	for i := 0; i < 300; i++ {
		pages = append(pages, Page{Index: i, Text: "t"})
	}
	out, trunc, next := Pages(pages, 50, 200)
	if len(out) != 50 || !trunc || next != 250 {
		t.Fatalf("len=%d trunc=%v next=%d", len(out), trunc, next)
	}
	if out[0].Index != 200 {
		t.Fatalf("primer índice = %d, esperaba 200", out[0].Index)
	}
}

// QA-065: el recorte local no toca FinishReason.
func TestCutPageKeepsFinishReason(t *testing.T) {
	p := Page{Index: 3, Text: strings.Repeat("a", 100), FinishReason: "stop"}
	out := CutPage(p, 10)
	if !out.PageTruncated || out.PageCharsKept != 10 || out.PageTotalChars != 100 {
		t.Fatalf("campos de recorte mal: %+v", out)
	}
	if out.FinishReason != "stop" {
		t.Fatalf("finish_reason alterado: %q", out.FinishReason)
	}
	untouched := CutPage(p, 200)
	if untouched.PageTruncated {
		t.Fatal("no debería recortar")
	}
}

// QA-065b: techo declarado.
func TestCeilings(t *testing.T) {
	_, _, total, _ := Text(strings.Repeat("a", 500), MaxCharsCeiling+1, 0)
	if total != 500 {
		t.Fatalf("total = %d", total)
	}
	var pages []Page
	for i := 0; i < 300; i++ {
		pages = append(pages, Page{Index: i})
	}
	out, _, _ := Pages(pages, MaxPagesCeiling+50, 0)
	if len(out) != MaxPagesCeiling {
		t.Fatalf("esperaba techo de %d páginas, obtuve %d", MaxPagesCeiling, len(out))
	}
}
