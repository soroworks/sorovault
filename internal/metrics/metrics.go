// Package metrics exposes SoroVault's operational state to Prometheus:
// what automatic refresh is doing, how many contracts are registered, and
// how the HTTP API is being used.
//
// Nothing here touches Prometheus's global registry. A caller builds a
// *prometheus.Registry, hands it to New, and serves it with Handler, which
// keeps tests isolated from each other and keeps the metrics off any port
// the caller did not choose.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/soroworks/sorovault/internal/refresher"
	"github.com/soroworks/sorovault/internal/store"
)

// namespace prefixes every metric this package defines.
const namespace = "sorovault"

// statsTimeout bounds the store query a scrape runs to count contracts, so
// a slow database delays a scrape rather than hanging it.
const statsTimeout = 5 * time.Second

// Metrics holds every collector serve feeds. It implements
// refresher.SweepObserver and api.RequestObserver.
type Metrics struct {
	sweeps      prometheus.Counter
	sweepErrors prometheus.Counter
	checked     prometheus.Counter
	changed     prometheus.Counter
	failed      prometheus.Counter
	busy        prometheus.Counter
	missing     prometheus.Counter
	lastSuccess prometheus.Gauge

	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// Options configures New.
type Options struct {
	// Store, when set, is counted on every scrape to report
	// sorovault_contracts_registered per network.
	Store store.Store
	// Logger receives scrape errors. Defaults to slog.Default.
	Logger *slog.Logger
	// ProcessCollectors adds the standard Go runtime and process
	// collectors (go_*, process_*). Serve turns it on; tests leave it off
	// so their output is only what this package defines.
	ProcessCollectors bool
}

// New registers SoroVault's collectors with reg and returns the handle the
// refresher and the API report through. It panics if a collector is
// already registered with reg, which is a programming error.
func New(reg prometheus.Registerer, opts Options) *Metrics {
	refresh := func(name, help string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "refresh", Name: name, Help: help,
		})
	}
	m := &Metrics{
		sweeps:      refresh("sweeps_total", "Scheduled refresh sweeps that completed."),
		sweepErrors: refresh("sweep_errors_total", "Scheduled refresh sweeps that could not run, such as when the RPC endpoint or the store was down."),
		checked:     refresh("contracts_checked_total", "Contracts a scheduled sweep attempted to refresh."),
		changed:     refresh("contracts_changed_total", "Contracts a scheduled sweep found upgraded since their last check."),
		failed:      refresh("contracts_failed_total", "Contracts a scheduled sweep could not refresh."),
		busy:        refresh("contracts_busy_total", "Contracts a scheduled sweep skipped because the registry was at its concurrency limit."),
		missing:     refresh("contracts_missing_total", "Contracts a scheduled sweep found gone from the network."),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "refresh", Name: "last_success_timestamp_seconds",
			Help: "Unix time the most recent scheduled sweep completed. Zero until one has.",
		}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "http", Name: "requests_total",
			Help: "HTTP requests served, by method, route pattern and status code.",
		}, []string{"method", "route", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
			Help:    "HTTP request latency, by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}

	reg.MustRegister(
		m.sweeps, m.sweepErrors, m.checked, m.changed, m.failed, m.busy, m.missing,
		m.lastSuccess, m.requests, m.duration,
	)
	if opts.Store != nil {
		log := opts.Logger
		if log == nil {
			log = slog.Default()
		}
		reg.MustRegister(&contractsCollector{store: opts.Store, log: log})
	}
	if opts.ProcessCollectors {
		reg.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		)
	}
	return m
}

// ObserveSweep implements refresher.SweepObserver.
func (m *Metrics) ObserveSweep(sum refresher.Summary, completedAt time.Time) {
	m.sweeps.Inc()
	m.checked.Add(float64(sum.Checked))
	m.changed.Add(float64(sum.Changed))
	m.failed.Add(float64(sum.Failed))
	m.busy.Add(float64(sum.Busy))
	m.missing.Add(float64(sum.Missing))
	m.lastSuccess.Set(float64(completedAt.UnixNano()) / 1e9)
}

// ObserveSweepError implements refresher.SweepObserver.
func (m *Metrics) ObserveSweepError() { m.sweepErrors.Inc() }

// ObserveRequest implements api.RequestObserver.
func (m *Metrics) ObserveRequest(method, route string, status int, duration time.Duration) {
	method = methodLabel(method)
	m.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(method, route).Observe(duration.Seconds())
}

// methodLabel bounds the method label. A client may send any token as a
// method, and each would otherwise become a series of its own.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

// Handler serves reg's metrics in the Prometheus exposition format. A
// collector that fails is reported in its place without failing the whole
// scrape, so a database outage does not also blind the refresh metrics.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		Registry:      reg,
	})
}

// contractsCollector reports how many contracts are registered per
// network. It counts at scrape time rather than tracking registrations,
// so the number is right whichever path — API, CLI, import — wrote them.
type contractsCollector struct {
	store store.Store
	log   *slog.Logger
}

var contractsDesc = prometheus.NewDesc(
	prometheus.BuildFQName(namespace, "", "contracts_registered"),
	"Contracts in the registry, by network.",
	[]string{"network"}, nil,
)

func (c *contractsCollector) Describe(ch chan<- *prometheus.Desc) { ch <- contractsDesc }

func (c *contractsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()

	stats, err := c.store.Stats(ctx)
	if err != nil {
		// Logged without the error: a store error can carry a connection
		// string, and the scrape output is not the place for one either.
		c.log.Warn("metrics: counting registered contracts failed")
		ch <- prometheus.NewInvalidMetric(contractsDesc, errContractsUnavailable)
		return
	}
	for _, n := range stats.Networks {
		ch <- prometheus.MustNewConstMetric(contractsDesc, prometheus.GaugeValue, float64(n.Contracts), n.Network)
	}
}

// errContractsUnavailable stands in for the store's error in the scrape
// output, which must not carry the detail the store's error might.
var errContractsUnavailable = errors.New("registered contracts could not be counted")

// Compile-time checks that Metrics is what serve hands it to.
var _ refresher.SweepObserver = (*Metrics)(nil)
