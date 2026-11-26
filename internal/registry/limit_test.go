package registry_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

// acquireTimeout is how long a test lets an operation wait for a slot
// before it is refused. It is short enough to keep the tests quick and
// long enough to be distinguishable from a scheduling hiccup.
const acquireTimeout = 100 * time.Millisecond

// blockingNetwork is a fake network whose fetches wait to be released, so
// that a test can hold registrations open and watch what the registry does
// with the ones that do not get a slot.
type blockingNetwork struct {
	*stellartest.Fake

	// entered counts the fetches that have started.
	entered chan struct{}
	// release is closed to let every held fetch finish.
	release chan struct{}
	// inFlight and peak track how many fetches run at once.
	inFlight atomic.Int64
	peak     atomic.Int64
}

// newBlockingNetwork returns a fake network whose fetches block until
// release is closed.
func newBlockingNetwork(t *testing.T) *blockingNetwork {
	t.Helper()

	b := &blockingNetwork{
		Fake:    stellartest.New(),
		entered: make(chan struct{}, 64),
		release: make(chan struct{}),
	}
	b.BeforeFetch = func(ctx context.Context, contractID string) error {
		inFlight := b.inFlight.Add(1)
		for {
			peak := b.peak.Load()
			if inFlight <= peak || b.peak.CompareAndSwap(peak, inFlight) {
				break
			}
		}
		defer b.inFlight.Add(-1)

		select {
		case b.entered <- struct{}{}:
		default:
		}

		select {
		case <-b.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b
}

// releaseAll lets every held fetch finish.
func (b *blockingNetwork) releaseAll() { close(b.release) }

// registry over the blocking network, with the given concurrency limit.
func (b *blockingNetwork) registry(t *testing.T, limit int) *registry.Registry {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return registry.New(b.Fake, spec.XDRDecoder{}, store.NewMemory(), registry.Options{
		Logger:         log,
		MaxConcurrent:  limit,
		AcquireTimeout: acquireTimeout,
	})
}

// TestRegisterRunsAtMostLimitAtOnce proves the bound itself: however many
// clients ask, no more than the limit register at the same time.
func TestRegisterRunsAtMostLimitAtOnce(t *testing.T) {
	t.Parallel()

	const limit = 2

	network := newBlockingNetwork(t)
	network.Deploy(idA, moduleWith(t, "hello"))
	reg := network.registry(t, limit)

	// Ask for more registrations than the limit allows.
	const extra = 3
	results := make(chan error, limit+extra)
	for range limit + extra {
		go func() {
			_, err := reg.Register(context.Background(), idA)
			results <- err
		}()
	}

	// The limit's worth of fetches start; no more than that.
	for range limit {
		select {
		case <-network.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a registration to start")
		}
	}
	select {
	case <-network.entered:
		t.Fatal("more registrations ran at once than the limit allows")
	case <-time.After(2 * acquireTimeout):
	}

	// The ones that did not get a slot are refused rather than queued.
	for range extra {
		select {
		case err := <-results:
			require.ErrorIs(t, err, registry.ErrBusy,
				"a registration that cannot get a slot must be refused")
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a registration to be refused")
		}
	}

	assert.Equal(t, int64(limit), network.peak.Load(),
		"the registry must not run more registrations at once than its limit")

	// Releasing the held fetches lets them finish.
	network.releaseAll()
	for range limit {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a registration to finish")
		}
	}
}

// TestRegisterReleasesCapacityAfterFailure proves a failed registration
// gives its slot back, so one bad contract cannot wedge the registry.
func TestRegisterReleasesCapacityAfterFailure(t *testing.T) {
	t.Parallel()

	network := newBlockingNetwork(t)
	network.Deploy(idA, moduleWith(t, "hello"))
	network.FetchErr = errors.New("the network is having a bad day")
	reg := network.registry(t, 1)

	done := make(chan error, 1)
	go func() {
		_, err := reg.Register(context.Background(), idA)
		done <- err
	}()

	// Let the registration reach the fetch, then fail.
	select {
	case <-network.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the registration to start")
	}
	network.releaseAll()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad day")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the registration to fail")
	}

	// The slot is free again: a second registration is not refused for
	// lack of capacity.
	network.FetchErr = nil
	_, err := reg.Register(context.Background(), idA)
	require.NoError(t, err, "a failed registration must not hold a slot")
}

// TestRegisterReleasesCapacityAfterCancellation proves a cancelled
// registration gives its slot back.
func TestRegisterReleasesCapacityAfterCancellation(t *testing.T) {
	t.Parallel()

	network := newBlockingNetwork(t)
	network.Deploy(idA, moduleWith(t, "hello"))
	reg := network.registry(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := reg.Register(ctx, idA)
		done <- err
	}()

	// Wait until the registration is inside the fetch, then cancel it.
	select {
	case <-network.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the registration to start")
	}
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the registration to be cancelled")
	}

	// The slot is free again.
	network.releaseAll()
	_, err := reg.Register(context.Background(), idA)
	require.NoError(t, err, "a cancelled registration must not hold a slot")
}

// TestRefreshSharesTheRegistrationBudget proves a refresh draws on the same
// budget as a registration: the refresher gets no more of the registry's
// capacity than an API client does.
func TestRefreshSharesTheRegistrationBudget(t *testing.T) {
	t.Parallel()

	network := newBlockingNetwork(t)
	network.Deploy(idA, moduleWith(t, "hello"))
	reg := network.registry(t, 1)

	// A registration takes the only slot and stays in it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = reg.Register(context.Background(), idA)
	}()

	select {
	case <-network.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the registration to start")
	}

	// A refresh wants the same slot, and is refused.
	_, err := reg.Refresh(context.Background(), idA)
	require.ErrorIs(t, err, registry.ErrBusy,
		"a refresh must not get capacity a registration is using")

	network.releaseAll()
	<-done
}

// TestRegistryReportsItsLimit proves the registry says what it is doing,
// so the API can tell a client how long to wait.
func TestRegistryReportsItsLimit(t *testing.T) {
	t.Parallel()

	reg, _, _ := newRegistry(t)
	assert.Equal(t, registry.DefaultMaxConcurrent, reg.MaxConcurrent())
	assert.Equal(t, registry.DefaultAcquireTimeout, reg.AcquireTimeout())
}

// TestCheckPassphrase proves the network guard can be asked directly, which
// is what the doctor command does.
func TestCheckPassphrase(t *testing.T) {
	t.Parallel()

	assert.NoError(t, registry.CheckPassphrase(stellar.NetworkTestnet, stellar.NetworkTestnet))
	assert.NoError(t, registry.CheckPassphrase(stellar.NetworkTestnet, ""),
		"an empty expectation accepts whatever the endpoint serves")

	err := registry.CheckPassphrase(stellar.NetworkPublic, stellar.NetworkTestnet)
	require.ErrorIs(t, err, registry.ErrNetworkMismatch)
	assert.Contains(t, err.Error(), stellar.NetworkPublic)
}

// TestCheckPassphraseEdgeCases pins the comparison at its boundaries: the
// match is exact, so near-misses must be refused, and only an empty
// expectation opts out of the guard.
func TestCheckPassphraseEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		got      string
		want     string
		mismatch bool
	}{
		{"both empty", "", "", false},
		{"empty expectation accepts anything", stellar.NetworkPublic, "", false},
		{"matching passphrases", stellar.NetworkTestnet, stellar.NetworkTestnet, false},
		{"empty report against an expectation", "", stellar.NetworkTestnet, true},
		{"case differs", "test sdf network ; september 2015", stellar.NetworkTestnet, true},
		{"trailing whitespace differs", stellar.NetworkTestnet + " ", stellar.NetworkTestnet, true},
		{"whitespace-only expectation is not empty", stellar.NetworkTestnet, " ", true},
		{"mismatch between public and testnet", stellar.NetworkPublic, stellar.NetworkTestnet, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := registry.CheckPassphrase(tt.got, tt.want)
			if tt.mismatch {
				require.ErrorIs(t, err, registry.ErrNetworkMismatch,
					"the passphrase comparison must be exact")
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestMaxConcurrentEdgeCases proves how New resolves the concurrency limit
// at its edges: an unset or invalid limit falls back to the default, and
// the smallest and very large valid limits are kept as given.
func TestMaxConcurrentEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "unset falls back to the default", limit: 0, want: registry.DefaultMaxConcurrent},
		{name: "negative falls back to the default", limit: -1, want: registry.DefaultMaxConcurrent},
		{name: "most negative int falls back to the default", limit: math.MinInt, want: registry.DefaultMaxConcurrent},
		{name: "one is the smallest limit kept", limit: 1, want: 1},
		{name: "a large limit is kept", limit: 1000, want: 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{
				Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
				MaxConcurrent: tt.limit,
			})
			assert.Equal(t, tt.want, reg.MaxConcurrent())
		})
	}
}

// TestMaxConcurrentOfOneSerialises proves the boundary limit of one is
// enforced, not merely reported: while one registration holds the only
// slot, a second is refused with ErrBusy.
func TestMaxConcurrentOfOneSerialises(t *testing.T) {
	t.Parallel()

	network := newBlockingNetwork(t)
	network.Deploy(idA, moduleWith(t, "hello"))
	reg := network.registry(t, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = reg.Register(context.Background(), idA)
	}()

	select {
	case <-network.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the registration to start")
	}

	_, err := reg.Register(context.Background(), idA)
	require.ErrorIs(t, err, registry.ErrBusy)

	network.releaseAll()
	<-done
	assert.EqualValues(t, 1, network.peak.Load())
}

// TestAcquireTimeoutEdgeCases mirrors TestMaxConcurrentEdgeCases for the
// AcquireTimeout option: an unset or non-positive value falls back to the
// default, and any positive value is kept as given.
func TestAcquireTimeoutEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "unset falls back to the default", timeout: 0, want: registry.DefaultAcquireTimeout},
		{name: "negative falls back to the default", timeout: -time.Second, want: registry.DefaultAcquireTimeout},
		{name: "most negative duration falls back to the default", timeout: time.Duration(math.MinInt), want: registry.DefaultAcquireTimeout},
		{name: "one nanosecond is the smallest timeout kept", timeout: time.Nanosecond, want: time.Nanosecond},
		{name: "a large timeout is kept", timeout: 10 * time.Minute, want: 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := registry.New(stellartest.New(), spec.XDRDecoder{}, store.NewMemory(), registry.Options{
				Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
				AcquireTimeout: tt.timeout,
			})
			assert.Equal(t, tt.want, reg.AcquireTimeout())
		})
	}
}
