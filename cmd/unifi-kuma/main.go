package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/johntdyer/unifi-kuma/internal/config"
	"github.com/johntdyer/unifi-kuma/internal/kuma"
	"github.com/johntdyer/unifi-kuma/internal/sync"
	"github.com/johntdyer/unifi-kuma/internal/unifi"
)

var version = "dev" // set by -ldflags at build time

func main() {
	var (
		cfgFile  = flag.String("config", "", "path to YAML config file (optional, env vars take precedence)")
		logLevel = flag.String("log-level", "info", "log level: debug, info, warn, error")
		logJSON  = flag.Bool("log-json", false, "output logs as JSON")
		ver      = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *ver {
		fmt.Printf("unifi-kuma %s\n", version)
		os.Exit(0)
	}

	setupLogger(*logLevel, *logJSON)

	cfg, err := config.Load(*cfgFile)
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	unifiClient, err := unifi.NewClient(cfg.UniFi.URL, cfg.UniFi.Site, cfg.UniFi.Insecure)
	if err != nil {
		slog.Error("failed to create UniFi client", "error", err)
		os.Exit(1)
	}
	if cfg.UniFi.APIKey != "" {
		unifiClient.SetAPIKey(cfg.UniFi.APIKey)
	}

	kumaClient := kuma.NewClient(cfg.Kuma.URL)
	if cfg.Kuma.APIKey != "" {
		kumaClient.SetAPIKey(cfg.Kuma.APIKey)
	}

	syncer := sync.New(cfg, unifiClient, kumaClient)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := syncer.Start(ctx); err != nil && err != context.Canceled {
		slog.Error("syncer exited with error", "error", err)
		os.Exit(1)
	}
}

func setupLogger(level string, jsonOutput bool) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if jsonOutput {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))
}
