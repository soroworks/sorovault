package metrics_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/api"
	"github.com/soroworks/sorovault/internal/metrics"
	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/refresher"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
	"github.com/soroworks/sorovault/internal/wasm"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

const (
	idA = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"
	idB = "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V"
	idC = "CBQHNAXSI55GX2GN6D67GK7BHVPSLJUGZQEU7WJ5LKR5PNUCGLIMAO4K"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// moduleWith builds a WASM module whose spec declares one function of the
// given name, so two versions of a contract decode differently.
func moduleWith(t *testing.T, fnName string) []byte {
	t.Helper()

	entry := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryFunctionV0,
		FunctionV0: &xdr.ScSpecFunctionV0{
			Name:    xdr.ScSymbol(fnName),
			Inputs:  []xdr.ScSpecFunctionInputV0{},
			Outputs: []xdr.ScSpecTypeDef{},
		},
	}
	body, err := entry.MarshalBinary()
	require.NoError(t, err)
	return wasmtest.Module(wasmtest.Section{Name: wasm.SectionContractSpec, Body: body})
}

// runOneSweep runs the refresher until its first scheduled sweep has been
// observed, then stops it. The clock is a day ahead so every contract is
// stale, and waiting is a no-op so the sweep does not pace itself.
func runOneSweep(t *testing.T, reg refresher.Registry, m *metrics.Metrics, now time.Time, done func() bool) {
	t.Helper()

	r, err := refresher.New(reg, refresher.Options{
		Interval: time.Hour,
		Observer: m,
		Logger:   discard(),
		Now:      func() time.Time { return now },
		Wait:     func(context.Context, time.Duration) error { return nil },
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = r.Run(ctx)
	}()
	require.Eventually(t, done, 5*time.Second, 5*time.Millisecond, "the sweep was never observed")
	cancel()
	<-stopped
}

// TestSweepMovesRefreshCounters proves a scheduled sweep over the fake
// network moves every refresh counter by what it did, and stamps the time
// of the last success.
func TestSweepMovesRefreshCounters(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	db := store.NewMemory()
	reg := registry.New(fake, spec.XDRDecoder{}, db, registry.Options{Logger: discard()})
	ctx := context.Background()

	// Three contracts: one will be upgraded, one will vanish, one stays.
	fake.Deploy(idA, moduleWith(t, "v1"))
	fake.Deploy(idB, moduleWith(t, "b"))
	fake.Deploy(idC, moduleWith(t, "c"))
	for _, id := range []string{idA, idB, idC} {
		_, err := reg.Register(ctx, id)
		require.NoError(t, err)
	}
	fake.Upgrade(idA, moduleWith(t, "v2"))
	fake.Remove(idB)

	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg, metrics.Options{})

	assert.Zero(t, gatherValue(t, promReg, "sorovault_refresh_sweeps_total"))
	assert.Zero(t, gatherValue(t, promReg, "sorovault_refresh_last_success_timestamp_seconds"))

	now := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	runOneSweep(t, reg, m, now, func() bool {
		return gatherValue(t, promReg, "sorovault_refresh_sweeps_total") >= 1
	})

	assert.Equal(t, 1.0, gatherValue(t, promReg, "sorovault_refresh_sweeps_total"))
	assert.Equal(t, 3.0, gatherValue(t, promReg, "sorovault_refresh_contracts_checked_total"))
	assert.Equal(t, 1.0, gatherValue(t, promReg, "sorovault_refresh_contracts_changed_total"))
	assert.Equal(t, 1.0, gatherValue(t, promReg, "sorovault_refresh_contracts_missing_total"))
	assert.Zero(t, gatherValue(t, promReg, "sorovault_refresh_contracts_failed_total"))
	assert.Zero(t, gatherValue(t, promReg, "sorovault_refresh_contracts_busy_total"))
	assert.Zero(t, gatherValue(t, promReg, "sorovault_refresh_sweep_errors_total"))
	assert.Equal(t, float64(now.Unix()), gatherValue(t, promReg, "sorovault_refresh_last_success_timestamp_seconds"))
}

// TestSweepCountsFailures proves a contract the network refuses is counted
// as failed, without failing the sweep.
func TestSweepCountsFailures(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	reg := registry.New(fake, spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: discard()})
	fake.Deploy(idA, moduleWith(t, "a"))
	_, err := reg.Register(context.Background(), idA)
	require.NoError(t, err)
	fake.BeforeFetch = func(context.Context, string) error { return errors.New("rpc exploded") }

	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg, metrics.Options{})
	runOneSweep(t, reg, m, time.Now().Add(24*time.Hour), func() bool {
		return gatherValue(t, promReg, "sorovault_refresh_sweeps_total") >= 1
	})

	assert.Equal(t, 1.0, gatherValue(t, promReg, "sorovault_refresh_contracts_checked_total"))
	assert.Equal(t, 1.0, gatherValue(t, promReg, "sorovault_refresh_contracts_failed_total"))
}

// brokenNetwork is a registry whose network cannot be resolved, so no sweep
// can work out what to refresh.
type brokenNetwork struct{ refresher.Registry }

func (brokenNetwork) Network(context.Context) (string, error) {
	return "", errors.New("rpc down")
}

// TestSweepErrorIsCounted proves a sweep that cannot run moves the error
// counter and leaves the success counter and timestamp alone, which is
// what an alert on a stale last-success time relies on.
func TestSweepErrorIsCounted(t *testing.T) {
	t.Parallel()

	reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{Logger: discard()})
	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg, metrics.Options{})

	runOneSweep(t, brokenNetwork{reg}, m, time.Now(), func() bool {
		return gatherValue(t, promReg, "sorovault_refresh_sweep_errors_total") >= 1
	})

	assert.Zero(t, gatherValue(t, promReg, "sorovault_refresh_sweeps_total"))
	assert.Zero(t, gatherValue(t, promReg, "sorovault_refresh_last_success_timestamp_seconds"))
}

func emptyInterface() *model.Interface {
	return &model.Interface{ABIVersion: model.ABIVersion}
}

// TestContractsRegisteredPerNetwork proves the gauge counts the store at
// scrape time, per network, whichever path registered the contracts.
func TestContractsRegisteredPerNetwork(t *testing.T) {
	t.Parallel()

	db := store.NewMemory()
	ctx := context.Background()
	require.NoError(t, db.Save(ctx, store.Contract{ContractID: idA, Network: "testnet", CurrentWasmHash: "h1"}, emptyInterface()))
	require.NoError(t, db.Save(ctx, store.Contract{ContractID: idB, Network: "testnet", CurrentWasmHash: "h2"}, emptyInterface()))
	require.NoError(t, db.Save(ctx, store.Contract{ContractID: idA, Network: "public", CurrentWasmHash: "h1"}, emptyInterface()))

	promReg := prometheus.NewRegistry()
	metrics.New(promReg, metrics.Options{Store: db, Logger: discard()})

	want := `
# HELP sorovault_contracts_registered Contracts in the registry, by network.
# TYPE sorovault_contracts_registered gauge
sorovault_contracts_registered{network="public"} 1
sorovault_contracts_registered{network="testnet"} 2
`
	require.NoError(t, testutil.GatherAndCompare(promReg, strings.NewReader(want), "sorovault_contracts_registered"))

	// A contract registered later is counted on the next scrape.
	require.NoError(t, db.Save(ctx, store.Contract{ContractID: idC, Network: "public", CurrentWasmHash: "h3"}, emptyInterface()))
	assert.Equal(t, 2, int(gatherLabelled(t, promReg, "sorovault_contracts_registered", "network", "public")))
}

// failingStats is a store whose statistics query fails.
type failingStats struct{ store.Store }

func (failingStats) Stats(context.Context) (*store.Stats, error) {
	return nil, errors.New("postgres://user:secret@db/sorovault unreachable")
}

// TestScrapeSurvivesAStoreOutage proves a failing store does not take the
// rest of the metrics down with it, and that its error — which may carry a
// connection string — is not served.
func TestScrapeSurvivesAStoreOutage(t *testing.T) {
	t.Parallel()

	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg, metrics.Options{Store: failingStats{store.NewMemory()}, Logger: discard()})
	m.ObserveSweep(refresher.Summary{Checked: 4}, time.Unix(1700000000, 0))

	srv := httptest.NewServer(metrics.Handler(promReg))
	t.Cleanup(srv.Close)

	body := scrape(t, srv.URL)
	assert.Contains(t, body, "sorovault_refresh_contracts_checked_total 4")
	assert.Contains(t, body, "sorovault_refresh_last_success_timestamp_seconds 1.7e+09")
	assert.NotContains(t, body, "sorovault_contracts_registered{")
	assert.NotContains(t, body, "secret")
}

// TestHTTPMetricsByRoute proves requests are counted and timed by route
// pattern rather than raw path, and that the values a client controls —
// the path of an unmatched request, the method — cannot mint new series.
func TestHTTPMetricsByRoute(t *testing.T) {
	t.Parallel()

	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg, metrics.Options{})

	r := chi.NewRouter()
	r.Use(api.Middleware(discard(), m)...)
	r.Get("/api/contracts/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Post("/api/contracts", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	r.Get("/silent", func(http.ResponseWriter, *http.Request) {})
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	do := func(method, path string) {
		req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, nil)
		require.NoError(t, err)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	do(http.MethodGet, "/api/contracts/"+idA)
	do(http.MethodGet, "/api/contracts/"+idB)
	do(http.MethodPost, "/api/contracts")
	do(http.MethodGet, "/silent")
	do(http.MethodGet, "/boom")
	do(http.MethodGet, "/no/such/"+idA)
	do("BREW", "/api/contracts/"+idA)

	want := `
# HELP sorovault_http_requests_total HTTP requests served, by method, route pattern and status code.
# TYPE sorovault_http_requests_total counter
sorovault_http_requests_total{code="200",method="GET",route="/api/contracts/{id}"} 2
sorovault_http_requests_total{code="200",method="GET",route="/silent"} 1
sorovault_http_requests_total{code="201",method="POST",route="/api/contracts"} 1
sorovault_http_requests_total{code="404",method="GET",route="unmatched"} 1
sorovault_http_requests_total{code="405",method="OTHER",route="unmatched"} 1
sorovault_http_requests_total{code="500",method="GET",route="/boom"} 1
`
	require.NoError(t, testutil.GatherAndCompare(promReg, strings.NewReader(want), "sorovault_http_requests_total"))

	assert.Equal(t, 6, seriesCount(t, promReg, "sorovault_http_request_duration_seconds"),
		"latency is kept per method and route, not per status")
	assert.NotContains(t, scrapeRegistry(t, promReg), idA, "no contract ID reaches a label")
}

// TestRegistriesAreIndependent proves two Metrics on separate registries
// do not share state, which is what keeps tests (and processes embedding
// several servers) isolated.
func TestRegistriesAreIndependent(t *testing.T) {
	t.Parallel()

	first, second := prometheus.NewRegistry(), prometheus.NewRegistry()
	m1 := metrics.New(first, metrics.Options{})
	metrics.New(second, metrics.Options{})

	m1.ObserveSweep(refresher.Summary{Checked: 1}, time.Now())
	assert.Equal(t, 1.0, gatherValue(t, first, "sorovault_refresh_sweeps_total"))
	assert.Zero(t, gatherValue(t, second, "sorovault_refresh_sweeps_total"))
}

// gatherValue is the value of an unlabelled metric, or zero when it has no
// samples.
func gatherValue(t *testing.T, g prometheus.Gatherer, name string) float64 {
	t.Helper()
	return gatherLabelled(t, g, name, "", "")
}

// gatherLabelled is the value of the sample of name whose label key has
// value; an empty key selects the first sample.
func gatherLabelled(t *testing.T, g prometheus.Gatherer, name, key, value string) float64 {
	t.Helper()

	families, err := g.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, sample := range f.GetMetric() {
			if key != "" {
				matched := false
				for _, l := range sample.GetLabel() {
					if l.GetName() == key && l.GetValue() == value {
						matched = true
					}
				}
				if !matched {
					continue
				}
			}
			switch {
			case sample.GetCounter() != nil:
				return sample.GetCounter().GetValue()
			case sample.GetGauge() != nil:
				return sample.GetGauge().GetValue()
			}
		}
	}
	return 0
}

// seriesCount is how many label combinations name has.
func seriesCount(t *testing.T, g prometheus.Gatherer, name string) int {
	t.Helper()

	families, err := g.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == name {
			return len(f.GetMetric())
		}
	}
	return 0
}

func scrape(t *testing.T, url string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(raw)
}

func scrapeRegistry(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()

	srv := httptest.NewServer(metrics.Handler(reg))
	defer srv.Close()
	return scrape(t, srv.URL)
}
