package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

func TestStartMetricsNoOpWhenDisabled(t *testing.T) {
	t.Parallel()

	a := &app{cfg: &config.Config{}}
	stop, err := startMetrics(context.Background(), a)
	require.NoError(t, err)
	assert.Nil(t, a.metrics, "nothing reports to metrics that are off")
	stop()
}

// freeAddr returns a loopback address nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// TestStartMetricsServesOnItsOwnAddress proves METRICS_ADDR gets a listener
// of its own serving GET /metrics — including the refresher's, the API's
// and the registry's metrics — and that stopping it closes the port.
func TestStartMetricsServesOnItsOwnAddress(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := store.NewMemory()
	a := &app{
		cfg:      &config.Config{MetricsAddr: freeAddr(t)},
		log:      log,
		registry: registry.New(stellartest.New(), spec.XDRDecoder{}, db, registry.Options{Logger: log}),
	}

	stop, err := startMetrics(context.Background(), a)
	require.NoError(t, err)
	require.NotNil(t, a.metrics)

	a.metrics.ObserveRequest(http.MethodGet, "/api/stats", http.StatusOK, time.Millisecond)

	status, body := get(t, "http://"+a.cfg.MetricsAddr+"/metrics")
	require.Equal(t, http.StatusOK, status)
	for _, want := range []string{
		"sorovault_refresh_sweeps_total 0",
		"sorovault_refresh_last_success_timestamp_seconds 0",
		`sorovault_http_requests_total{code="200",method="GET",route="/api/stats"} 1`,
		"go_goroutines",
	} {
		assert.Contains(t, body, want)
	}

	status, _ = get(t, "http://"+a.cfg.MetricsAddr+"/")
	assert.Equal(t, http.StatusNotFound, status, "the metrics listener serves nothing else")

	stop()
	var d net.Dialer
	_, err = d.DialContext(context.Background(), "tcp", a.cfg.MetricsAddr)
	assert.Error(t, err, "stopping closes the listener")
}

// TestStartMetricsFailsOnABusyPort proves a port already in use fails serve
// at startup rather than leaving metrics silently absent.
func TestStartMetricsFailsOnABusyPort(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	busy, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &app{
		cfg:      &config.Config{MetricsAddr: busy.Addr().String()},
		log:      log,
		registry: registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: log}),
	}
	_, err = startMetrics(context.Background(), a)
	require.ErrorContains(t, err, "metrics listener")
}

// TestRouterFeedsHTTPMetrics proves serve's router reports requests by the
// full route pattern across the mounted API and UI routers.
func TestRouterFeedsHTTPMetrics(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &app{
		cfg:      &config.Config{MetricsAddr: freeAddr(t)},
		log:      log,
		registry: registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: log}),
	}
	stop, err := startMetrics(context.Background(), a)
	require.NoError(t, err)
	t.Cleanup(stop)

	handler, err := newRouterWith(a.registry, log, nil, a.cfg, a.metrics)
	require.NoError(t, err)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	status, _ := get(t, srv.URL+"/api/contracts/CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN")
	require.Equal(t, http.StatusNotFound, status)
	status, _ = get(t, srv.URL+"/healthz")
	require.Equal(t, http.StatusOK, status)

	_, body := get(t, "http://"+a.cfg.MetricsAddr+"/metrics")
	assert.Contains(t, body, `sorovault_http_requests_total{code="404",method="GET",route="/api/contracts/{id}"} 1`)
	assert.Contains(t, body, `sorovault_http_requests_total{code="200",method="GET",route="/healthz"} 1`)
}
