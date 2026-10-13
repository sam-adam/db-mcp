package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/example/readonly-db-mcp/internal/config"
	"github.com/example/readonly-db-mcp/internal/manager"
	"github.com/example/readonly-db-mcp/internal/mcp"
)

func main() {
	configPath := flag.String("config", "config.yaml", "configuration file")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(2)
	}
	mgr, err := manager.New(cfg, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "startup error:", err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer mgr.Close()
	if err := mcp.Serve(ctx, os.Stdin, os.Stdout, mgr, logger); err != nil && ctx.Err() == nil {
		logger.Error("MCP server stopped", "error", err)
		os.Exit(1)
	}
}
