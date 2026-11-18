// Package config resuelve la configuración del servidor a partir del entorno.
//
// La regla que gobierna este paquete es la del ADR-001: el servidor no muere por
// falta de configuración. Una clave ausente se convierte en un error accionable
// en la primera llamada, no en un proceso que no existe para el cliente MCP.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
)

// DefaultBaseURL es la que documenta la API de Tools de DeepSeek.
const DefaultBaseURL = "https://api.deepseek.com"

// KeyEnv y BaseURLEnv son las variables de entorno que se leen.
const (
	KeyEnv      = "DEEPSEEK_API_KEY"
	BaseURLEnv  = "DEEPSEEK_BASE_URL"
	LogLevelEnv = "DEEPSEEK_LOG_LEVEL"
)

// Config es la configuración resuelta del proceso.
type Config struct {
	// APIKey vacía significa "no configurado": el servidor arranca igual y las
	// tools devuelven un error de configuración en la primera llamada.
	APIKey   string
	BaseURL  string
	LogLevel slog.Level

	// Warnings son los problemas no fatales encontrados al cargar. Se escriben
	// en stderr al arrancar y no abortan nada.
	Warnings []string
}

// Configured informa si hay una clave utilizable.
func (c Config) Configured() bool { return strings.TrimSpace(c.APIKey) != "" }

// Load resuelve la configuración desde getenv. Recibe el lector de entorno como
// parámetro en vez de llamar a os.Getenv adentro: es lo que permite testearlo
// con un mapa y la razón por la que este paquete no necesita infraestructura.
func Load(getenv func(string) string) Config {
	cfg := Config{
		APIKey:   strings.TrimSpace(getenv(KeyEnv)),
		BaseURL:  DefaultBaseURL,
		LogLevel: slog.LevelWarn,
	}

	if raw := strings.TrimSpace(getenv(BaseURLEnv)); raw != "" {
		if normalized, err := normalizeBaseURL(raw); err != nil {
			cfg.Warnings = append(cfg.Warnings,
				fmt.Sprintf("%s = %q no es una URL HTTP/HTTPS absoluta (%v); se usa %s",
					BaseURLEnv, raw, err, DefaultBaseURL))
		} else {
			cfg.BaseURL = normalized
		}
	}

	if raw := strings.TrimSpace(getenv(LogLevelEnv)); raw != "" {
		if lvl, err := parseLevel(raw); err == nil {
			cfg.LogLevel = lvl
		} else {
			cfg.Warnings = append(cfg.Warnings,
				fmt.Sprintf("%s = %q no es un nivel válido; se usa warn", LogLevelEnv, raw))
		}
	}

	return cfg
}

// normalizeBaseURL valida esquema y host, y quita la barra final para que el
// armado de rutas no produzca dobles barras.
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("esquema %q no permitido", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("falta el host")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelWarn, fmt.Errorf("nivel %q desconocido", raw)
}

// Environ es el lector de entorno real, para usar en main.
func Environ(key string) string { return os.Getenv(key) }
