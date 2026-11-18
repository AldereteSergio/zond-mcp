package config

import (
	"log/slog"
	"testing"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// QA-001
func TestLoadWithKey(t *testing.T) {
	cfg := Load(env(map[string]string{KeyEnv: "sk-test"}))
	if !cfg.Configured() || cfg.APIKey != "sk-test" {
		t.Fatalf("esperaba clave configurada, obtuve %+v", cfg)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("base url default = %q", cfg.BaseURL)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("sin warnings esperados: %v", cfg.Warnings)
	}
}

// QA-002 QA-006: sin clave no hay error, y no existe vía alternativa.
func TestLoadWithoutKeyDoesNotFail(t *testing.T) {
	cfg := Load(env(map[string]string{}))
	if cfg.Configured() {
		t.Fatal("no debería estar configurado")
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("faltar la clave no es un warning de URL: %v", cfg.Warnings)
	}
	cfg = Load(env(map[string]string{"DEEPSEEK_API_KEY_FILE": "C:\algo"}))
	if cfg.Configured() {
		t.Fatal("no debe leerse un archivo de clave: esa vía no existe en el diseño")
	}
}

// QA-004
func TestLoadInvalidBaseURLFallsBack(t *testing.T) {
	for _, raw := range []string{"ftp://x.com", "sin-esquema", "https://", "://"} {
		cfg := Load(env(map[string]string{KeyEnv: "k", BaseURLEnv: raw}))
		if cfg.BaseURL != DefaultBaseURL {
			t.Fatalf("%q: esperaba default, obtuve %q", raw, cfg.BaseURL)
		}
		if len(cfg.Warnings) != 1 {
			t.Fatalf("%q: esperaba un warning, obtuve %v", raw, cfg.Warnings)
		}
	}
}

func TestLoadBaseURLTrimSlash(t *testing.T) {
	cfg := Load(env(map[string]string{BaseURLEnv: "https://gateway.local/v1/"}))
	if cfg.BaseURL != "https://gateway.local/v1" {
		t.Fatalf("base url = %q", cfg.BaseURL)
	}
}

// QA-005
func TestLoadLogLevel(t *testing.T) {
	cfg := Load(env(map[string]string{LogLevelEnv: "debug"}))
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("nivel = %v", cfg.LogLevel)
	}
	cfg = Load(env(map[string]string{LogLevelEnv: "verbose"}))
	if cfg.LogLevel != slog.LevelWarn {
		t.Fatalf("nivel inválido debería caer a warn, obtuve %v", cfg.LogLevel)
	}
	if len(cfg.Warnings) != 1 {
		t.Fatalf("esperaba warning por nivel inválido")
	}
}
