// Command zond-mcp expone los tres endpoints de Tools de DeepSeek como tools
// MCP, por stdio, como binario único.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldereteSergio/zond-mcp/internal/config"
	"github.com/AldereteSergio/zond-mcp/internal/deepseek"
	"github.com/AldereteSergio/zond-mcp/internal/faults"
	"github.com/AldereteSergio/zond-mcp/internal/mcpserver"
)

// Estos tres se pisan por ldflags en el release.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("zond-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showVersion := fs.Bool("version", false, "imprime versión, commit y fecha y sale")
	check := fs.Bool("check", false, "verifica configuración, credenciales y saldo, y sale")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Printf("zond-mcp %s (commit %s, %s)\n", version, commit, date)
		return 0
	}

	cfg := config.Load(config.Environ)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	for _, w := range cfg.Warnings {
		logger.Warn(w)
	}

	deepseek.Version = version
	client := deepseek.New(cfg.BaseURL, cfg.APIKey, logger)

	if *check {
		return runCheck(cfg, client)
	}

	if !cfg.Configured() {
		logger.Warn("falta DEEPSEEK_API_KEY: el servidor arranca, pero las tools van a devolver un error de configuración",
			"variable", config.KeyEnv)
	}

	server := mcpserver.Build(&mcp.Implementation{
		Name:    "zond-mcp",
		Title:   "DeepSeek Tools",
		Version: version,
	}, mcpserver.Deps{Client: client, Log: logger})

	// El transporte stdio usa stdout para el protocolo: todo diagnóstico va a
	// stderr, o se corrompe el canal.
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		logger.Error("el servidor terminó con error", "err", err)
		return 1
	}
	return 0
}

// runCheck valida la instalación sin gastar una consulta facturable: el
// endpoint de saldo no se cobra como request de tools.
func runCheck(cfg config.Config, client *deepseek.Client) int {
	if !cfg.Configured() {
		fmt.Fprintln(os.Stderr, "× configuración: falta "+config.KeyEnv+" en el env del mcp.json.")
		return 1
	}
	fmt.Fprintf(os.Stderr, "✓ configuración: clave presente, base %s\n", cfg.BaseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	balance, fault := client.CheckBalance(ctx)
	if fault != nil {
		if fault.Category == faults.CatNetwork {
			fmt.Fprintf(os.Stderr, "× conectividad: no se pudo llegar a %s (%s)\n", cfg.BaseURL, fault.Detail)
			return 1
		}
		fmt.Fprintf(os.Stderr, "× credenciales: %s\n", fault.Error())
		return 1
	}
	fmt.Fprintln(os.Stderr, "✓ conectividad: la API respondió")

	if strings.TrimSpace(balance.Total) == "" {
		fmt.Fprintln(os.Stderr, "× saldo: la API respondió pero no informó un saldo legible. Revisá la cuenta en platform.deepseek.com.")
		return 1
	}
	if !balance.Available || isZero(balance.Total) {
		fmt.Fprintf(os.Stderr, "× saldo: la cuenta no tiene crédito disponible (%s %s). Cargá saldo antes de usar las tools.\n",
			balance.Total, balance.Currency)
		return 1
	}
	fmt.Fprintf(os.Stderr, "✓ saldo: %s %s\n", balance.Total, balance.Currency)
	return 0
}

func isZero(s string) bool {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		// Un formato inesperado no se trata como cero: se deja pasar para no
		// inventar un fallo donde no lo hay.
		return false
	}
	return f <= 0
}
