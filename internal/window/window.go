// Package window aplica las ventanas de salida del servidor.
//
// La regla del ADR-001: ningún recorte silencioso. Toda ventana devuelve si
// recortó, el tamaño real del contenido y el puntero para continuar. Las
// funciones son puras y se testean con los bordes.
package window

import "unicode/utf8"

// DefaultMaxChars y DefaultMaxPages son los valores que usa el servidor cuando
// el llamador no pide otra cosa. No son defaults de la API: son ventanas
// nuestras, declaradas.
const (
	DefaultMaxChars = 50_000
	MaxCharsCeiling = 200_000
	DefaultMaxPages = 50
	MaxPagesCeiling = 200
)

// Text recorta s a maxChars caracteres (runas, no bytes) desde offset.
// Devuelve el trozo, si recortó, el total de caracteres del texto completo y el
// offset del siguiente trozo. next es -1 cuando no hay más.
func Text(s string, maxChars, offset int) (chunk string, truncated bool, total int, next int) {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	if maxChars > MaxCharsCeiling {
		maxChars = MaxCharsCeiling
	}

	total = utf8.RuneCountInString(s)

	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return "", false, total, -1
	}

	// Se recorre por índices de byte para no duplicar el string completo en
	// memoria: una respuesta de 60 MiB convertida a []rune pediría 240 MiB.
	start := byteIndexAfterRunes(s, offset)
	end := offset + maxChars
	if end >= total {
		return s[start:], false, total, -1
	}
	return s[start:byteIndexAfterRunes(s, end)], true, total, end
}

// byteIndexAfterRunes devuelve el índice de byte del comienzo de la runa número
// n (0-based). Si n supera la cantidad de runas, devuelve len(s).
func byteIndexAfterRunes(s string, n int) int {
	if n <= 0 {
		return 0
	}
	count := 0
	for i := range s {
		if count == n {
			return i
		}
		count++
	}
	return len(s)
}

// Page es una página reconocida por OCR. Index y FinishReason son campos de la
// API y viajan intactos; los campos de recorte local llevan nombre propio.
type Page struct {
	Index          int
	Text           string
	FinishReason   string // "stop" o "length", tal como lo mandó la API
	PageTruncated  bool   // recorte local de esta página
	PageCharsKept  int    // caracteres que se entregan de esta página (0 = todos)
	PageTotalChars int    // caracteres que traía esta página
}

// Pages selecciona una ventana de páginas desde fromPage. Conserva los índices
// originales del documento, así que una cita "página 200" sigue siendo válida
// aunque la ventana empiece ahí.
func Pages(pages []Page, maxPages, fromPage int) (out []Page, truncated bool, next int) {
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	if maxPages > MaxPagesCeiling {
		maxPages = MaxPagesCeiling
	}
	if fromPage < 0 {
		fromPage = 0
	}
	if fromPage >= len(pages) {
		return nil, false, -1
	}

	end := fromPage + maxPages
	if end >= len(pages) {
		return pages[fromPage:], false, -1
	}
	return pages[fromPage:end], true, end
}

// CutPage recorta el texto de una página al límite de caracteres sin tocar
// FinishReason, que es de la API y significa otra cosa.
func CutPage(p Page, maxChars int) Page {
	if maxChars <= 0 || utf8.RuneCountInString(p.Text) <= maxChars {
		return p
	}
	p.PageTotalChars = utf8.RuneCountInString(p.Text)
	p.PageTruncated = true
	p.PageCharsKept = maxChars
	p.Text = p.Text[:byteIndexAfterRunes(p.Text, maxChars)]
	return p
}
