package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/johntdyer/unifi-kuma/internal/metrics"
)

// newTestHandler builds the same mux New would, but on an httptest server
// so tests don't need a real TCP listener/port.
func newTestHandler(t *testing.T, m *metrics.Metrics, maxAge time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", healthzHandler(m, maxAge))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestHealthz_NoSyncYet(t *testing.T) {
	m := metrics.New("dev")
	ts := newTestHandler(t, m, time.Hour)

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var body healthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.False(t, body.OK)
	assert.Contains(t, body.Detail, "no sync")
}

func TestHealthz_HealthyAfterSuccess(t *testing.T) {
	m := metrics.New("dev")
	m.RecordSync(time.Second, nil)
	ts := newTestHandler(t, m, time.Hour)

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body healthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.True(t, body.OK)
}

func TestHealthz_UnhealthyAfterFailure(t *testing.T) {
	m := metrics.New("dev")
	m.RecordSync(time.Second, errors.New("boom"))
	ts := newTestHandler(t, m, time.Hour)

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestMetrics_ExposesCustomAndStandardMetrics(t *testing.T) {
	m := metrics.New("1.2.3")
	m.RecordSync(time.Second, nil)
	m.StaleClients.Set(3)
	ts := newTestHandler(t, m, time.Hour)

	resp, err := http.Get(ts.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	text := string(body)

	assert.Contains(t, text, "unifi_kuma_stale_clients 3")
	assert.Contains(t, text, `unifi_kuma_build_info{version="1.2.3"} 1`)
	assert.Contains(t, text, "unifi_kuma_syncs_total")
	// Standard process/Go collectors registered alongside the custom ones.
	assert.Contains(t, text, "go_goroutines")
	assert.Contains(t, text, "process_start_time_seconds")
}

func TestServer_StartAndShutdown(t *testing.T) {
	m := metrics.New("dev")
	s := New("127.0.0.1:0", m, time.Hour)

	errCh := make(chan error, 1)
	go func() { errCh <- s.Start() }()

	// Give the listener a moment to come up before shutting down; Start
	// returning http.ErrServerClosed (rather than a bind error) is what we
	// actually need to verify.
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, s.Shutdown(ctx))

	err := <-errCh
	assert.ErrorIs(t, err, http.ErrServerClosed)
}
