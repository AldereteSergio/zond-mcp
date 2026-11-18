package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"github.com/AldereteSergio/zond-mcp/internal/deepseek"
	"github.com/AldereteSergio/zond-mcp/internal/faults"
	"github.com/AldereteSergio/zond-mcp/internal/window"
)

// Límites documentados en api-ocr-guia.txt.
const (
	OCRMaxBytes       = 100 << 20 // 100 MiB
	OCRMaxPages       = 1000
	PurposeText       = "text_recognition"
	PurposeRichLayout = "rich_layout_recognition"
)

// OCRParams es la entrada de la tool ocr.
type OCRParams struct {
	Path     string
	Purpose  string
	FromPage int
	MaxPages int
}

// Validate revisa propósito y ventana. El peso se valida al abrir el archivo.
func (p OCRParams) Validate() *faults.Fault {
	switch p.Purpose {
	case PurposeText, PurposeRichLayout:
	case "":
		return faults.InvalidInput("Falta purpose.",
			"Mandá text_recognition (extrae el texto directamente de Word y PowerPoint sin imágenes) o rich_layout_recognition (reconoce todas las páginas respetando el layout).")
	default:
		return faults.InvalidInput(
			fmt.Sprintf("purpose = %q no es válido.", p.Purpose),
			"Los valores posibles son text_recognition y rich_layout_recognition.")
	}
	if p.MaxPages < 0 || p.MaxPages > window.MaxPagesCeiling {
		return faults.InvalidInput(
			fmt.Sprintf("max_pages vale %d y el máximo es %d.", p.MaxPages, window.MaxPagesCeiling),
			fmt.Sprintf("Usá un valor entre 0 y %d; si lo omitís se usan %d.", window.MaxPagesCeiling, window.DefaultMaxPages))
	}
	if p.FromPage < 0 {
		return faults.InvalidInput("from_page no puede ser negativo.", "Usá el next_page que devolvió la llamada anterior.")
	}
	return nil
}

// OpenFile valida y abre el archivo. Deja el handle abierto para el llamador.
//
// El chequeo de symlink es explícito porque os.Open los resuelve siempre: sin
// Lstat, prometer "no seguimos symlinks" sería mentir.
func OpenFile(path string) (*os.File, int64, *faults.Fault) {
	if path == "" {
		return nil, 0, faults.InvalidInput("Falta path.", "Pasá la ruta de un archivo local.")
	}
	if fault := rejectSymlinks(path); fault != nil {
		return nil, 0, fault
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, faults.InvalidInput(
			fmt.Sprintf("No se pudo leer %q: %v", path, err),
			"Verificá que la ruta exista y que tengas permiso de lectura.")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, faults.InvalidInput(
			fmt.Sprintf("%q es un enlace simbólico.", path),
			"Pasá la ruta real del archivo; el servidor no sigue enlaces para no leer fuera de lo indicado.")
	}
	if !info.Mode().IsRegular() {
		return nil, 0, faults.InvalidInput(
			fmt.Sprintf("%q no es un archivo regular.", path),
			"Pasá la ruta de un archivo: no directorios, ni dispositivos, ni tuberías.")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, faults.InvalidInput(
			fmt.Sprintf("No se pudo abrir %q: %v", path, err),
			"Verificá los permisos del archivo.")
	}
	// Stat sobre el handle: el archivo puede haber cambiado desde el Lstat.
	handleInfo, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, faults.InvalidInput("No se pudo consultar el tamaño del archivo.", "Probá copiarlo a otra ruta y volver a intentar.")
	}
	if handleInfo.Size() > OCRMaxBytes {
		f.Close()
		return nil, 0, faults.InvalidInput(
			fmt.Sprintf("El archivo pesa %d MiB y el máximo es 100 MiB.", handleInfo.Size()>>20),
			"Comprimilo o partilo en partes de 100 MiB o menos.")
	}
	return f, handleInfo.Size(), nil
}

// rejectSymlinks recorre la ruta componente por componente.
//
// Lstat sobre la ruta completa solo mira el último componente: con
// `/tmp/link/doc.pdf`, donde `link` es un symlink a otro directorio, el archivo
// final es regular y el enlace se sigue igual. Para que la promesa del ADR sea
// cierta hay que revisar cada directorio del camino.
//
// Límite conocido y documentado: en Windows esto detecta enlaces simbólicos,
// pero no necesariamente junctions u otros reparse points.
func rejectSymlinks(path string) *faults.Fault {
	abs, err := filepath.Abs(path)
	if err != nil {
		return faults.InvalidInput(
			fmt.Sprintf("No se pudo resolver la ruta %q: %v", path, err),
			"Pasá una ruta absoluta o relativa al directorio de trabajo actual.")
	}
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.Trim(rest, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return faults.InvalidInput(
				fmt.Sprintf("No se pudo leer %q: %v", current, err),
				"Verificá que la ruta exista y que tengas permiso de lectura.")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return faults.InvalidInput(
				fmt.Sprintf("%q pasa por el enlace simbólico %q.", path, current),
				"Pasá la ruta real del archivo; el servidor no sigue enlaces, ni en el archivo ni en sus directorios.")
		}
	}
	return nil
}

// BuildMultipart arma el cuerpo con exactamente dos campos: file y purpose.
// Cualquier campo extra hace que la API devuelva 400.
func BuildMultipart(f *os.File, name, purpose string) deepseek.OCRBody {
	var boundary string
	{
		w := multipart.NewWriter(io.Discard)
		boundary = w.Boundary()
		w.Close()
	}

	build := func() (io.Reader, error) {
		// Cada intento necesita el archivo desde el principio: si se reintenta,
		// el handle ya está al final y el segundo multipart saldría vacío.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		pr, pw := io.Pipe()
		go func() {
			mw := multipart.NewWriter(pw)
			if err := mw.SetBoundary(boundary); err != nil {
				pw.CloseWithError(err)
				return
			}
			part, err := mw.CreateFormFile("file", name)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			// Se lee por streaming y con tope: nunca se carga el archivo dos
			// veces en memoria, y si creció mientras tanto no se envía más de
			// lo permitido.
			if _, err := io.Copy(part, io.LimitReader(f, OCRMaxBytes)); err != nil {
				pw.CloseWithError(err)
				return
			}
			if err := mw.WriteField("purpose", purpose); err != nil {
				pw.CloseWithError(err)
				return
			}
			pw.CloseWithError(mw.Close())
		}()
		return pr, nil
	}

	return deepseek.OCRBody{ContentType: "multipart/form-data; boundary=" + boundary, Build: build}
}

// OCRPage es una página tal como la devuelve la API.
type OCRPage struct {
	Index        int    `json:"index"`
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
}

// OCRResult es la respuesta cruda de la API.
type OCRResult struct {
	ID        string    `json:"id"`
	PageCount int       `json:"page_count"`
	Pages     []OCRPage `json:"pages"`
}

// OCRResponse es la respuesta de la tool, con la ventana aplicada.
type OCRResponse struct {
	ID         string
	PageCount  int
	FromPage   int
	Pages      []window.Page
	NextPage   int
	Truncated  bool
	CostUSD    float64
	CutWarning bool
}

// ParseOCR decodifica la respuesta de la API.
func ParseOCR(raw []byte) (OCRResult, *faults.Fault) {
	var parsed OCRResult
	if err := json.Unmarshal(trimJSON(raw), &parsed); err != nil {
		return OCRResult{}, faults.Classify(faults.ToolOCR, 200, nil, err)
	}
	return parsed, nil
}

// ApplyPageWindow arma la respuesta de la tool: ventana por páginas, índices
// originales y recorte por página con nombres propios.
func ApplyPageWindow(res OCRResult, p OCRParams) OCRResponse {
	pages := make([]window.Page, 0, len(res.Pages))
	for _, pg := range res.Pages {
		pages = append(pages, window.Page{
			Index:        pg.Index,
			Text:         pg.Text,
			FinishReason: pg.FinishReason,
		})
	}
	out, truncated, next := window.Pages(pages, p.MaxPages, p.FromPage)

	cut := false
	for i := range out {
		before := out[i].PageTruncated
		out[i] = window.CutPage(out[i], window.DefaultMaxChars)
		if out[i].PageTruncated && !before {
			cut = true
		}
	}

	return OCRResponse{
		ID:         res.ID,
		PageCount:  res.PageCount,
		FromPage:   p.FromPage,
		Pages:      out,
		NextPage:   next,
		Truncated:  truncated,
		CostUSD:    costPerOCR(res.PageCount),
		CutWarning: cut,
	}
}

func costPerRequest() float64 { return deepseek.PricePerRequestUSD }

func costPerOCR(pages int) float64 {
	// Se factura por página reconocida; para el total del documento se usa el
	// page_count completo, no el de la ventana.
	return float64(pages) * deepseek.PricePerPageUSD
}
