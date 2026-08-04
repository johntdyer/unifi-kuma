package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johntdyer/unifi-kuma/internal/config"
	"github.com/johntdyer/unifi-kuma/internal/httpserver"
	"github.com/johntdyer/unifi-kuma/internal/kuma"
	"github.com/johntdyer/unifi-kuma/internal/sync"
	"github.com/johntdyer/unifi-kuma/internal/unifi"
)

var version = "dev" // set by -ldflags at build time

func main() {
	var (
		cfgFile     = flag.String("config", "", "path to YAML config file (optional, env vars take precedence)")
		logLevel    = flag.String("log-level", "info", "log level: debug, info, warn, error")
		logJSON     = flag.Bool("log-json", false, "output logs as JSON")
		ver         = flag.Bool("version", false, "print version and exit")
		healthcheck = flag.Bool("healthcheck", false, "check /healthz on the running instance's HTTP server and exit 0/1 accordingly (for use as a Docker HEALTHCHECK against this same binary)")
	)
	flag.Parse()

	if *ver {
		fmt.Printf("unifi-kuma %s\n", version)
		os.Exit(0)
	}

	if *healthcheck {
		os.Exit(runHealthcheck(*cfgFile))
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
	unifiClient.SetStaleWarnAfter(cfg.Sync.StaleWarnAfter)

	kumaClient := kuma.NewClient(cfg.Kuma.URL)
	if cfg.Kuma.DisableAuth {
		kumaClient.SetNoAuth()
	}
	defer func() {
		if err := kumaClient.Close(); err != nil {
			slog.Error("failed to close Kuma connection", "error", err)
		}
	}()

	syncer := sync.New(cfg, unifiClient, kumaClient)
	syncer.Metrics().SetVersion(version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpSrv := startHTTPServer(cfg, syncer)

	syncErr := syncer.Start(ctx)

	if httpSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("http server shutdown failed", "error", err)
		}
	}

	if syncErr != nil && syncErr != context.Canceled {
		slog.Error("syncer exited with error", "error", syncErr)
		os.Exit(1)
	}
}

// startHTTPServer starts the /healthz and /metrics server in the
// background unless HTTP_ADDR is empty (disabled). The readiness window
// for /healthz is derived from the sync interval rather than separately
// configured: a successful sync older than 3 cycles indicates the loop is
// stuck even though the process itself is still running.
func startHTTPServer(cfg *config.Config, syncer *sync.Syncer) *httpserver.Server {
	if cfg.HTTP.Addr == "" {
		return nil
	}

	healthMaxAge := 3 * cfg.Sync.Interval
	srv := httpserver.New(cfg.HTTP.Addr, syncer.Metrics(), healthMaxAge)

	go func() {
		slog.Info("starting http server", "addr", cfg.HTTP.Addr)
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "error", err)
		}
	}()

	return srv
}

// runHealthcheck queries this same instance's /healthz endpoint and
// returns a process exit code (0 healthy, 1 not) — meant to be invoked as
// `unifi-kuma -healthcheck` from a Docker HEALTHCHECK, since the distroless
// base image has no shell or curl/wget to run one any other way. cfgFile is
// whatever -config was passed (usually empty), so a healthcheck against an
// instance configured via a YAML file finds the same HTTP_ADDR it did. It
// loads config the normal way (same env as the running instance, since
// Docker HEALTHCHECK runs inside the same container) purely to find
// HTTP_ADDR's port; it never touches UniFi or Kuma.
func runHealthcheck(cfgFile string) int {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: failed to load configuration: %v\n", err)
		return 1
	}

	if cfg.HTTP.Addr == "" {
		fmt.Fprintln(os.Stderr, "healthcheck: HTTP_ADDR is empty (server disabled); nothing to check")
		return 0
	}

	_, port, err := net.SplitHostPort(cfg.HTTP.Addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: invalid HTTP_ADDR %q: %v\n", cfg.HTTP.Addr, err)
		return 1
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: request failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: unhealthy, status %d\n", resp.StatusCode)
		return 1
	}
	return 0
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
