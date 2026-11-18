// Package faults traduce fallos de la API y del transporte a una taxonomía
// cerrada con texto accionable.
//
// Es la pieza que separa a este servidor de un pasamanos: el agente recibe "qué
// pasó" y "qué hacer", no un código HTTP. Y la reintentabilidad es una
// propiedad del fallo, no un juicio de cada llamador.
package faults

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Tool identifica la tool que originó el fallo. La clasificación depende de la
// tool: un 404 en fetch no es lo mismo que un 404 en otro lado, y un 429 de OCR
// no es un límite de tasa.
type Tool string

const (
	ToolSearch Tool = "search"
	ToolFetch  Tool = "fetch"
	ToolOCR    Tool = "ocr"
)

// Category es la taxonomía cerrada del ADR-001.
type Category string

const (
	CatConfiguration Category = "configuration"
	CatInvalidInput  Category = "invalid_input"
	CatAuth          Category = "auth"
	CatPayment       Category = "payment"
	CatPolicy        Category = "policy"
	CatUnusable      Category = "unusable"
	CatRateLimit     Category = "rate_limit"
	CatTransient     Category = "transient"
	CatTimeout       Category = "timeout"
	CatNetwork       Category = "network"
	CatCancelled     Category = "cancelled"
	CatInternal      Category = "internal"
)

// Fault es un fallo ya traducido.
type Fault struct {
	Category   Category
	HTTPStatus int    // 0 si el fallo no tuvo respuesta HTTP
	Message    string // qué pasó
	Action     string // qué hacer
	Retryable  bool
	Detail     string // cuerpo o error original, para depurar
}

// Error implementa error con la forma "qué pasó. qué hacer (código)".
func (f *Fault) Error() string {
	return fmt.Sprintf("%s %s (%s)", f.Message, f.Action, f.Code())
}

// Code es el identificador técnico que va entre paréntesis al final.
func (f *Fault) Code() string {
	if f.HTTPStatus > 0 {
		return fmt.Sprintf("%s/%d", f.Category, f.HTTPStatus)
	}
	return string(f.Category)
}

// Landed informa si el fallo llegó a la API. Los que no llegaron no se
// facturan, y eso importa para no asustar al usuario con gastos que no existen.
func (f *Fault) Landed() bool { return f.HTTPStatus > 0 }

// InvalidInput construye un fallo de entrada inválida detectado localmente.
// No hubo request, así que no hay costo.
func InvalidInput(message, action string) *Fault {
	return &Fault{
		Category: CatInvalidInput,
		Message:  message,
		Action:   action,
	}
}

// NotConfigured es el fallo de la primera llamada cuando falta la clave. Existe
// para que el servidor pueda arrancar sin ella.
func NotConfigured() *Fault {
	return &Fault{
		Category: CatConfiguration,
		Message:  "No hay clave de API configurada.",
		Action:   "Poné DEEPSEEK_API_KEY en el bloque env del mcp.json y reiniciá el cliente.",
	}
}

// wiredCodes son los errores que la API documenta con un code estable.
var wiredCodes = map[string]struct {
	cat       Category
	action    string
	retryable bool
}{
	"invalid_request_error": {CatInvalidInput, "Corregí el pedido: la API rechazó un campo.", false},
	"gateway_timeout_error": {CatTimeout, "El servicio no terminó a tiempo; probá con menos contenido.", false},
	"insufficient_balance":  {CatPayment, "Cargá crédito en platform.deepseek.com; reintentar no cambia nada.", false},
	"rate_limit_error":      {CatRateLimit, "Esperá unos segundos y volvé a intentar.", true},
	"authentication_error":  {CatAuth, "Revisá la clave en platform.deepseek.com.", false},
}

// ocrMessages son los mensajes exactos de la tabla de errores de la guía de
// OCR. Se clasifican por igualdad, no por prefijo: un parser de prefijos de
// inglés se rompe con la primera paráfrasis del proveedor.
var ocrMessages = map[string]struct {
	cat       Category
	action    string
	retryable bool
}{
	"The file is password-protected and cannot be processed. Remove the password and upload it again.": {
		CatUnusable, "Quitá la contraseña del archivo y volvé a intentar.", false},
	"Failed to recognize the file: the file could not be parsed or its format is not supported": {
		CatUnusable, "Convertí el archivo a un formato soportado y volvé a intentar.", false},
	"Failed to recognize the file: the file format is not supported": {
		CatUnusable, "Convertí el archivo a PDF, Word, PowerPoint o una imagen soportada.", false},
	"Failed to recognize the file: the image could not be decoded": {
		CatUnusable, "El archivo está dañado o no es una imagen válida; probá con otro.", false},
	"Failed to recognize the file: the document has more than 1000 pages": {
		CatInvalidInput, "El documento supera las 1000 páginas; partilo en partes de 1000 o menos.", false},
	"file is too large: max 100 MiB": {
		CatInvalidInput, "El archivo supera los 100 MiB; comprimilo o partilo.", false},
	"OCR did not finish in time": {
		CatTimeout, "No reintentes: el reconocimiento sigue corriendo y se cobra si termina. Mandá un archivo más chico.", false},
	"OCR did not finish within 180 seconds": {
		CatTimeout, "No reintentes: el reconocimiento sigue corriendo y se cobra si termina. Mandá un archivo más chico.", false},
	"The OCR service is temporarily unavailable. Please try again later.": {
		CatTransient, "Esperá unos segundos y volvé a intentar.", true},
	"Internal Server Error": {
		CatTransient, "Esperá unos segundos y volvé a intentar.", true},
}

// apiError es la forma del objeto error de la API (compatible con OpenAI).
type apiError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Classify es la única puerta de entrada de la taxonomía. Es pura: no hace I/O
// ni guarda memoria de intentos (el campo `retried` lo agrega la capa de
// orquestación, no esta función).
//
// Orden de decisión: error de transporte, error contenido en el cuerpo (aunque
// el status sea 200), tabla de mensajes exactos, código documentado, tabla de
// status.
func Classify(tool Tool, status int, body []byte, transportErr error) *Fault {
	if transportErr != nil {
		return classifyTransport(transportErr)
	}

	parsed := parseAPIError(body)

	// Con un status de error, el status manda: la tabla de códigos del cuerpo
	// incluye `invalid_request_error` para cosas que no son de entrada, y un
	// 401 clasificado como "corregí el pedido" manda al usuario al lugar
	// equivocado. El mensaje del cuerpo se usa cuando es más específico.
	if status >= 400 {
		if parsed != nil {
			if f := classifyMessage(tool, parsed.Error.Message); f != nil {
				f.HTTPStatus = status
				f.Detail = parsed.Error.Message
				return f
			}
		}
		return classifyStatus(tool, status, parsed)
	}

	// Un 200 con `error` adentro es falla. El criterio es la presencia del
	// campo, nunca la ausencia del esperado: un documento sin páginas es un
	// resultado válido y tratarlo como fallo sería inventar un error.
	if parsed != nil {
		if f := classifyMessage(tool, parsed.Error.Message); f != nil {
			f.HTTPStatus = status
			f.Detail = parsed.Error.Message
			// Un error dentro de un 200 ya consumió el trabajo: reintentarlo
			// vuelve a facturar. Nunca se marca reintentable.
			f.Retryable = false
			return f
		}
		if f := classifyCode(parsed.Error.Code); f != nil {
			f.HTTPStatus = status
			f.Detail = parsed.Error.Message
			return f
		}
		return &Fault{
			Category:   CatInternal,
			HTTPStatus: status,
			Message:    "La API devolvió un error no documentado.",
			Action:     "Reportalo con este detalle.",
			Detail:     parsed.Error.Message,
		}
	}

	return nil
}

func classifyTransport(err error) *Fault {
	switch {
	case errors.Is(err, context.Canceled):
		return &Fault{
			Category: CatCancelled,
			Message:  "La llamada se canceló.",
			Action:   "No se reintenta; si querés el resultado, volvé a llamar.",
		}
	case errors.Is(err, context.DeadlineExceeded):
		return &Fault{
			Category:  CatTimeout,
			Message:   "Se agotó el tiempo de espera.",
			Action:    "Reintentá con menos contenido o probá de nuevo.",
			Retryable: true,
			Detail:    err.Error(),
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) || isNetworkText(err.Error()) {
		return &Fault{
			Category:  CatNetwork,
			Message:   "No se pudo conectar con api.deepseek.com.",
			Action:    "Revisá la conexión o el proxy de tu red.",
			Retryable: true,
			Detail:    err.Error(),
		}
	}
	return &Fault{
		Category: CatInternal,
		Message:  "Falló el transporte antes de llegar a la API.",
		Action:   "Reportalo con este detalle.",
		Detail:   err.Error(),
	}
}

func isNetworkText(s string) bool {
	for _, frag := range []string{"connection refused", "no such host", "dial tcp", "tls:", "EOF", "proxyconnect", "network is unreachable"} {
		if strings.Contains(s, frag) {
			return true
		}
	}
	return false
}

func classifyMessage(tool Tool, message string) *Fault {
	// La tabla de mensajes es de OCR: son textos que el servicio de
	// reconocimiento documenta. Aplicarla a search o fetch sería adivinar.
	if tool != ToolOCR || message == "" {
		return nil
	}
	if entry, ok := ocrMessages[message]; ok {
		return &Fault{Category: entry.cat, Message: message, Action: entry.action, Retryable: entry.retryable}
	}
	return nil
}

func classifyCode(code string) *Fault {
	if code == "" {
		return nil
	}
	if entry, ok := wiredCodes[code]; ok {
		return &Fault{Category: entry.cat, Action: entry.action, Retryable: entry.retryable,
			Message: "La API rechazó el pedido con " + code + "."}
	}
	return nil
}

func classifyStatus(tool Tool, status int, parsed *apiError) *Fault {
	message := ""
	if parsed != nil {
		message = parsed.Error.Message
	}
	mk := func(cat Category, msg, action string, retryable bool) *Fault {
		if message != "" && msg == "" {
			msg = message
		}
		return &Fault{Category: cat, HTTPStatus: status, Message: msg, Action: action, Retryable: retryable, Detail: message}
	}

	switch status {
	case 400:
		return mk(CatInvalidInput, "La API rechazó el pedido.", "Corregí el campo que indica el detalle y volvé a intentar.", false)
	case 401:
		return mk(CatAuth, "La clave de API es inválida o falta.", "Revisá DEEPSEEK_API_KEY en el bloque env del mcp.json.", false)
	case 402:
		return mk(CatPayment, "La cuenta no tiene saldo.", "Cargá crédito en platform.deepseek.com; reintentar no cambia nada.", false)
	case 403:
		return mk(CatPolicy, "El sitio rechazó la lectura por política de seguridad.", "Probá con otra URL; reintentar no cambia nada.", false)
	case 404:
		if tool == ToolFetch {
			return mk(CatUnusable, "No se pudo extraer contenido de esa URL.",
				"Puede estar caída, bloqueada por el sitio, devolver un tipo no soportado o no tener Markdown extraíble. Probá otra fuente.", false)
		}
		return mk(CatUnusable, "El recurso no existe.", "Verificá el pedido.", false)
	case 408:
		return mk(CatTransient, "La API tardó demasiado en recibir el pedido.", "Reintentá; si se repite, mandá menos contenido.", true)
	case 413:
		return mk(CatInvalidInput, "El cuerpo del pedido es demasiado grande.", "Mandá un archivo o un pedido más chico.", false)
	case 429:
		if tool == ToolOCR {
			return mk(CatRateLimit, "La cuenta ya tiene demasiados reconocimientos en curso.",
				"Esperá a que terminen los que están corriendo; reintentar ahora vuelve a fallar.", false)
		}
		return mk(CatRateLimit, "Se excedió el límite de pedidos por segundo.", "El servidor ya esperó; si se repite, bajá el ritmo.", true)
	case 500, 502, 503:
		return mk(CatTransient, "El servicio falló del lado de DeepSeek.", "Esperá unos segundos y volvé a intentar.", true)
	case 504:
		return mk(CatTimeout, "El servicio no respondió a tiempo.", "Reintentá; si se repite, mandá menos contenido.", true)
	case 200:
		return nil
	}
	if status >= 500 {
		return mk(CatTransient, "El servicio falló del lado de DeepSeek.", "Esperá unos segundos y volvé a intentar.", true)
	}
	return mk(CatInternal, "La API devolvió un estado no documentado.", "Reportalo con este detalle.", false)
}

func parseAPIError(body []byte) *apiError {
	if len(body) == 0 {
		return nil
	}
	var parsed apiError
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	if parsed.Error.Message == "" && parsed.Error.Code == "" && parsed.Error.Type == "" {
		return nil
	}
	return &parsed
}
