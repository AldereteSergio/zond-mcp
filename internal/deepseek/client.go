// Package deepseek es el cliente HTTP de los endpoints de Tools.
//
// No decide política: recibe el fallo ya clasificado y aplica la tabla de
// reintentos del ADR-001. Los timeouts están elegidos para no ser nunca más
// cortos que los de la API.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/AldereteSergio/zond-mcp/internal/faults"
)

// Precios publicados por DeepSeek el 2026-11-16, verificados en
// https://api-docs.deepseek.com/guides/search y https://api-docs.deepseek.com/guides/ocr
// (sección Billing de cada una). Si DeepSeek los cambia, se actualizan acá y en
// el README: viven en un solo lugar para que el cambio sea visible.
const (
	PricePerRequestUSD = 0.00075  // search y fetch, por request exitosa
	PricePerPageUSD    = 0.000225 // OCR, por página reconocida
	PricesAsOf         = "2026-11-16"
)

// Timeouts. El de OCR es mayor al de la API (180 s) a propósito: con un timeout
// propio más corto se paga el trabajo igual y no se recibe el resultado.
const (
	JSONTimeout = 60 * time.Second
	OCRTimeout  = 200 * time.Second
	maxAttempts = 2
	retry429    = 2 * time.Second
	retry5xx    = 500 * time.Millisecond
)

// Client habla con la API de Tools.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	log     *slog.Logger

	// ocrSem limita los reconocimientos simultáneos de este proceso. No
	// replica el tope de quince de la cuenta: evita la ráfaga propia.
	ocrSem chan struct{}

	// sleep es inyectable para que los tests de reintento no esperen de verdad.
	sleep func(ctx context.Context, d time.Duration) error
}

// New construye el cliente.
func New(baseURL, apiKey string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: JSONTimeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		log:    log,
		ocrSem: make(chan struct{}, 10),
		sleep:  sleepCtx,
	}
}

// Configured informa si el cliente tiene una clave utilizable. El servidor
// arranca igual cuando no la tiene; la primera llamada devuelve el error.
func (c *Client) Configured() bool { return c != nil && strings.TrimSpace(c.apiKey) != "" }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Search llama a POST /v1/tools/search y devuelve el cuerpo crudo.
func (c *Client) Search(ctx context.Context, payload []byte) ([]byte, *faults.Fault) {
	return c.do(ctx, faults.ToolSearch, "/v1/tools/search", func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/tools/search", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

// Fetch llama a POST /v1/tools/fetch y devuelve el cuerpo crudo.
func (c *Client) Fetch(ctx context.Context, payload []byte) ([]byte, *faults.Fault) {
	return c.do(ctx, faults.ToolFetch, "/v1/tools/fetch", func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/tools/fetch", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

// OCRBody arma el cuerpo multipart. Se pasa como fábrica porque un request que
// se reintenta necesita un cuerpo nuevo.
type OCRBody struct {
	ContentType string
	Build       func() (io.Reader, error)
}

// OCR llama a POST /v1/tools/ocr.
//
// La llamada se desacopla del contexto del tool call: corre con su propio
// timeout, y si el cliente canceló el turno se espera igual el resultado y se
// registra en stderr con el id. Desconectar no cancela el reconocimiento del
// lado de DeepSeek, así que abortar acá solo tira un resultado que ya se cobró.
func (c *Client) OCR(ctx context.Context, body OCRBody) ([]byte, *faults.Fault) {
	select {
	case c.ocrSem <- struct{}{}:
		defer func() { <-c.ocrSem }()
	default:
		return nil, &faults.Fault{
			Category: faults.CatRateLimit,
			Message:  "Este proceso ya tiene diez reconocimientos en curso.",
			Action:   "Esperá a que terminen; no se envió nada a la API.",
		}
	}

	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), OCRTimeout)
	defer cancel()

	raw, fault := c.do(detached, faults.ToolOCR, "/v1/tools/ocr", func() (*http.Request, error) {
		reader, err := body.Build()
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(detached, http.MethodPost, c.baseURL+"/v1/tools/ocr", reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", body.ContentType)
		// Sin ContentLength: el multipart siempre mide más que el archivo, así
		// que anunciar el tamaño del archivo haría que net/http cortara el
		// cuerpo a mitad de camino. Chunked es lo correcto acá.
		return req, nil
	})

	if ctx.Err() != nil && raw != nil {
		// El cliente cortó pero el reconocimiento terminó igual: se cobró.
		id := extractID(raw)
		c.log.Log(context.WithoutCancel(ctx), slog.LevelInfo,
			"reconocimiento completado después de que el cliente canceló",
			"ocr_id", id, "bytes", len(raw), "cobrado", true)
	}
	return raw, fault
}

// Balance consulta GET /user/balance. Es la única llamada fuera de
// /v1/tools/* y no factura como request de tools; la usa --check.
type Balance struct {
	Available bool   `json:"is_available"`
	Total     string `json:"total_balance"`
	Currency  string `json:"currency"`
	Raw       string `json:"-"`
}

type balanceResponse struct {
	Available bool `json:"is_available"`
	Infos     []struct {
		Currency     string `json:"currency"`
		TotalBalance string `json:"total_balance"`
	} `json:"balance_infos"`
}

// CheckBalance resuelve saldo y disponibilidad. Un 402 o un 401 vuelven como
// fallo de la taxonomía, igual que en las tools.
func (c *Client) CheckBalance(ctx context.Context) (Balance, *faults.Fault) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/user/balance", nil)
	if err != nil {
		return Balance{}, faults.Classify(faults.ToolSearch, 0, nil, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return Balance{}, faults.Classify(faults.ToolSearch, 0, nil, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Balance{}, faults.Classify(faults.ToolSearch, resp.StatusCode, raw, nil)
	}
	var parsed balanceResponse
	if err := jsonUnmarshal(raw, &parsed); err != nil {
		return Balance{}, faults.Classify(faults.ToolSearch, resp.StatusCode, raw, err)
	}
	out := Balance{Available: parsed.Available, Raw: string(raw)}
	if len(parsed.Infos) > 0 {
		out.Total = parsed.Infos[0].TotalBalance
		out.Currency = parsed.Infos[0].Currency
	}
	return out, nil
}

// do aplica la política de reintentos del ADR-001.
//
// Reintenta solo lo que la tabla marca como reintentable, con techo de dos
// intentos, espera fija y respeto de la cancelación del contexto tanto en la
// espera como entre intentos.
func (c *Client) do(ctx context.Context, tool faults.Tool, path string, build func() (*http.Request, error)) ([]byte, *faults.Fault) {
	var lastFault *faults.Fault
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, faults.Classify(tool, 0, nil, err)
		}

		req, err := build()
		if err != nil {
			return nil, faults.Classify(tool, 0, nil, err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("User-Agent", userAgent())

		started := time.Now()
		resp, err := c.http.Do(req)
		if err != nil {
			lastFault = faults.Classify(tool, 0, nil, err)
		} else {
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
			resp.Body.Close()
			if readErr != nil {
				lastFault = faults.Classify(tool, 0, nil, readErr)
			} else {
				lastFault = faults.Classify(tool, resp.StatusCode, raw, nil)
				if lastFault == nil {
					c.log.Log(ctx, slog.LevelInfo, "llamada ok",
						"tool", tool, "path", path, "status", resp.StatusCode,
						"ms", time.Since(started).Milliseconds(), "attempt", attempt, "bytes", len(raw))
					return raw, nil
				}
				if resp.StatusCode == http.StatusOK {
					// 200 con error adentro: se registra y se devuelve como falla.
					c.log.Log(ctx, slog.LevelWarn, "la API devolvió un error dentro de un 200",
						"tool", tool, "category", lastFault.Category, "status", resp.StatusCode)
					return raw, lastFault
				}
			}
		}

		if !lastFault.Retryable || attempt == maxAttempts {
			break
		}
		wait := retry5xx
		if lastFault.Category == faults.CatRateLimit {
			wait = retry429
		}
		if err := c.sleep(ctx, wait); err != nil {
			return nil, faults.Classify(tool, 0, nil, err)
		}
	}

	c.log.Log(ctx, slog.LevelWarn, "llamada fallida",
		"tool", tool, "category", lastFault.Category, "status", lastFault.HTTPStatus)
	return nil, lastFault
}

// maxResponseBytes es el tope de lectura del cuerpo. La auditoría de comparables
// encontró que search con count 50 y need_content puede superar los 10 MiB, que
// es el límite por defecto de varios clientes HTTP; acá se lee con tope propio.
const maxResponseBytes = 64 << 20

func userAgent() string { return "zond-mcp/" + Version }

// Version la setea main desde el ldflag de compilación.
var Version = "dev"

func extractID(raw []byte) string {
	var probe struct {
		ID string `json:"id"`
	}
	if err := jsonUnmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.ID
}

func jsonUnmarshal(data []byte, v any) error {
	// El cuerpo de OCR viene precedido de saltos de línea de keep-alive; como
	// son espacios en blanco válidos al inicio de un JSON, alcanza con recortar.
	return json.Unmarshal(bytes.TrimLeft(data, " \t\r\n"), v)
}
