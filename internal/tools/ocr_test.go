package tools

import (
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AldereteSergio/zond-mcp/internal/window"
)

func TestPurposeValidation(t *testing.T) {
	if f := (OCRParams{Path: "x", Purpose: ""}).Validate(); f == nil {
		t.Fatal("purpose vacío debe fallar: no tiene default")
	}
	if f := (OCRParams{Path: "x", Purpose: "otro"}).Validate(); f == nil {
		t.Fatal("purpose inválido debe fallar")
	}
	for _, p := range []string{PurposeText, PurposeRichLayout} {
		if f := (OCRParams{Path: "x", Purpose: p}).Validate(); f != nil {
			t.Fatalf("%q debería pasar: %v", p, f)
		}
	}
	if f := (OCRParams{Path: "x", Purpose: PurposeText, MaxPages: 500}).Validate(); f == nil {
		t.Fatal("max_pages sobre el techo debe fallar")
	}
}

// QA-019
func TestOpenFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, _, f := OpenFile(filepath.Join(dir, "no-existe.pdf")); f == nil {
		t.Fatal("ruta inexistente")
	}
	if _, _, f := OpenFile(dir); f == nil {
		t.Fatal("directorio")
	}
	if _, _, f := OpenFile(""); f == nil {
		t.Fatal("ruta vacía")
	}
}

// QA-020: se valida el peso sin leer el contenido.
func TestOpenFileTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grande.pdf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(OCRMaxBytes + 1024); err != nil {
		f.Close()
		t.Skip("no se pudo crear archivo disperso:", err)
	}
	f.Close()

	if _, _, fault := OpenFile(path); fault == nil {
		t.Fatal("101 MiB debería rechazarse")
	}
}

// QA-021: un symlink se rechaza. En Windows puede requerir privilegios.
func TestOpenFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.pdf")
	if err := os.WriteFile(real, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.pdf")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("sin permiso para crear symlinks:", err)
	}
	if _, _, fault := OpenFile(link); fault == nil {
		t.Fatal("un symlink debería rechazarse")
	}
	if _, _, fault := OpenFile(real); fault != nil {
		t.Fatalf("el archivo real debería aceptarse: %v", fault)
	}
}

// QA-032: multipart con exactamente dos campos.
func TestMultipartHasExactlyTwoFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4 contenido"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, _, fault := OpenFile(path)
	if fault != nil {
		t.Fatal(fault)
	}
	defer file.Close()

	body := BuildMultipart(file, "doc.pdf", PurposeText)
	reader, err := body.Build()
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(body.ContentType)
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(reader, params["boundary"])
	fields := map[string]string{}
	var gotFile bool
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(part)
		fields[part.FormName()] = string(data)
		if part.FormName() == "file" {
			gotFile = part.FileName() == "doc.pdf"
		}
	}
	if len(fields) != 2 || !gotFile || fields["purpose"] != PurposeText {
		t.Fatalf("campos=%v archivo=%v", fields, gotFile)
	}
	if !strings.Contains(fields["file"], "%PDF") {
		t.Fatalf("el contenido no viajó: %q", fields["file"])
	}
}

// QA-064, QA-065: ventana por páginas e índices originales.
func TestApplyPageWindow(t *testing.T) {
	var pages []OCRPage
	for i := 0; i < 5; i++ {
		pages = append(pages, OCRPage{Index: i, Text: strings.Repeat("a", 10), FinishReason: "stop"})
	}
	pages[3].Text = strings.Repeat("b", window.DefaultMaxChars+10)
	pages[3].FinishReason = "length"

	res := OCRResult{ID: "ocr-1", PageCount: 5, Pages: pages}
	out := ApplyPageWindow(res, OCRParams{FromPage: 2, MaxPages: 2})
	if len(out.Pages) != 2 || out.Pages[0].Index != 2 {
		t.Fatalf("ventana mal: %+v", out.Pages)
	}
	if !out.Truncated || out.NextPage != 4 {
		t.Fatalf("truncado mal: %+v", out)
	}

	full := ApplyPageWindow(res, OCRParams{})
	if full.PageCount != 5 {
		t.Fatalf("page_count = %d", full.PageCount)
	}
	if !full.CutWarning {
		t.Fatal("la página 3 excede el límite y debería marcarse")
	}
	if full.Pages[3].FinishReason != "length" {
		t.Fatalf("finish_reason alterado: %q", full.Pages[3].FinishReason)
	}
	if !full.Pages[3].PageTruncated {
		t.Fatal("la página recortada debería declararlo")
	}
}

// MED-2: un symlink en un directorio del camino también se rechaza.
func TestOpenFileRejectsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(real, "doc.pdf")
	if err := os.WriteFile(doc, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("sin permiso para crear symlinks:", err)
	}
	if _, _, fault := OpenFile(filepath.Join(link, "doc.pdf")); fault == nil {
		t.Fatal("una ruta que pasa por un symlink debería rechazarse")
	}
	if _, _, fault := OpenFile(doc); fault != nil {
		t.Fatalf("la ruta real debería aceptarse: %v", fault)
	}
}
