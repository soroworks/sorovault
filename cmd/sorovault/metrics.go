package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/soroworks/sorovault/internal/metrics"
)

// startMetrics serves Prometheus metrics on METRICS_ADDR, on a listener of
// its own, and points a.metrics at what the refresher and the API report
// through. With METRICS_ADDR unset it does nothing: metrics are opt-in,
// and never share the registry's public port.
//
// The address is bound before startMetrics returns, so a port already in
// use fails serve at startup instead of leaving metrics quietly absent.
// The returned stop function shuts the listener down.
func startMetrics(ctx context.Context, a *app) (stop func(), err error) {
	if a.cfg.MetricsAddr == "" {
		return func() {}, nil
	}

	reg := prometheus.NewRegistry()
	a.metrics = metrics.New(reg, metrics.Options{
		Store:             a.registry.Store(),
		Logger:            a.log.With("component", "metrics"),
		ProcessCollectors: true,
	})

	return serveMetrics(ctx, a.cfg.MetricsAddr, metrics.Handler(reg), a.log)
}

// serveMetrics binds addr and serves GET /metrics from handler until stop
// is called.
func serveMetrics(ctx context.Context, addr string, handler http.Handler, log *slog.Logger) (stop func(), err error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics listener: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", handler)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		log.Info("serving metrics", "addr", ln.Addr().String(), "path", "/metrics")
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics listener stopped", "error", err)
		}
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-done
	}, nil
}
