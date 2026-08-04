// Package httpserver exposes unifi-kuma's /healthz and /metrics endpoints.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/johntdyer/unifi-kuma/internal/metrics"
)

// Server serves /healthz and /metrics on its own listener, independent of
// the sync loop.
type Server struct {
	http *http.Server
}

// New builds a Server listening on addr. healthMaxAge is passed straight
// through to Metrics.Healthy for /healthz — see its docs.
func New(addr string, m *metrics.Metrics, healthMaxAge time.Duration) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", healthzHandler(m, healthMaxAge))

	return &Server{
		http: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// healthResponse is the JSON body /healthz returns.
type healthResponse struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func healthzHandler(m *metrics.Metrics, maxAge time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		ok, detail := m.Healthy(maxAge)

		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(healthResponse{OK: ok, Detail: detail})
	}
}

// Start blocks serving until the listener fails or Shutdown is called, at
// which point it returns http.ErrServerClosed — the caller should treat
// that specific error as a clean shutdown, not a failure.
func (s *Server) Start() error {
	return s.http.ListenAndServe()
}

// Shutdown gracefully stops the server, waiting for in-flight requests to
// finish (bounded by ctx).
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
