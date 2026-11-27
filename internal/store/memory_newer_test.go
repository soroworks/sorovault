package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
)

// These IDs are ordered lowest to highest, which lets the tie-break tests
// state their expectation in terms of the ordering Postgres uses:
// "ORDER BY first_seen DESC, contract_id".
const (
	newerIDLow   = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"
	newerIDHigh  = "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V"
	newerIDLater = "CDZZZZYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V"
)

// newerTestIface builds the smallest interface the store will accept.
func newerTestIface(fn string) *model.Interface {
	return &model.Interface{Functions: []model.Function{{Name: fn}}}
}

// TestNewerContractOrdersByObservationThenID pins the comparison used to
// choose Stats.Latest. Postgres asks the same question with
// "ORDER BY first_seen DESC, contract_id LIMIT 1", so a later observation
// wins and an equal timestamp is settled by the lower contract ID.
func TestNewerContractOrdersByObservationThenID(t *testing.T) {
	t.Parallel()

	require.Less(t, newerIDLow, newerIDHigh)
	require.Less(t, newerIDHigh, newerIDLater)

	earlier := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Second)

	low := Contract{ContractID: newerIDLow, Network: "testnet", FirstSeen: earlier}
	high := Contract{ContractID: newerIDHigh, Network: "testnet", FirstSeen: earlier}
	latest := Contract{ContractID: newerIDLater, Network: "testnet", FirstSeen: later}

	tests := []struct {
		name   string
		a, b   Contract
		wantAB bool
		wantBA bool
	}{
		{"later observation wins", latest, low, true, false},
		{"earlier observation loses", low, latest, false, true},
		{"equal timestamps prefer lower ID", low, high, true, false},
		{"equal timestamps reject higher ID", high, low, false, true},
		{"the same row is not newer", low, low, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantAB, newerContract(tt.a, tt.b))
			assert.Equal(t, tt.wantBA, newerContract(tt.b, tt.a),
				"the answer must be deterministic whichever order the arguments arrive in")
		})
	}
}

// TestMemoryStatsLatestMatchesPostgresOrder exercises the tie-break through
// the store. Contracts registered at the same instant must resolve to the
// same row Postgres's ORDER BY would return, regardless of insertion order.
func TestMemoryStatsLatestMatchesPostgresOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m := NewMemory()

	tied := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	m.SetClock(func() time.Time { return tied })

	// Save the higher ID first so insertion order cannot accidentally
	// produce the expected answer.
	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDHigh, Network: "testnet", CurrentWasmHash: "hash-high"},
		newerTestIface("high")))
	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDLow, Network: "testnet", CurrentWasmHash: "hash-low"},
		newerTestIface("low")))

	stats, err := m.Stats(ctx)
	require.NoError(t, err)
	require.NotNil(t, stats.Latest)
	assert.Equal(t, newerIDLow, stats.Latest.ContractID,
		"Postgres breaks an equal first_seen tie with the lower contract_id")
	assert.True(t, stats.Latest.FirstSeen.Equal(tied))

	// A genuinely later registration beats the tie-break, even though its
	// ID sorts after both of the tied contracts.
	m.SetClock(func() time.Time { return tied.Add(time.Minute) })
	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDLater, Network: "testnet", CurrentWasmHash: "hash-later"},
		newerTestIface("later")))

	stats, err = m.Stats(ctx)
	require.NoError(t, err)
	require.NotNil(t, stats.Latest)
	assert.Equal(t, newerIDLater, stats.Latest.ContractID, "a later observation wins")
	assert.True(t, stats.Latest.FirstSeen.After(tied))
}

// TestMemoryTiedVersionsStayConsistentWithGet documents what happens when two
// interface versions are observed at the same instant. The list must contain
// both versions, and the one it marks Current must be exactly what GetSpec
// returns as the current interface, so the two views cannot disagree.
func TestMemoryTiedVersionsStayConsistentWithGet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m := NewMemory()

	tied := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	m.SetClock(func() time.Time { return tied })

	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDLow, Network: "testnet", CurrentWasmHash: "hash-v1"},
		newerTestIface("v1")))
	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDLow, Network: "testnet", CurrentWasmHash: "hash-v2"},
		newerTestIface("v2")))

	versions, err := m.ListVersions(ctx, "testnet", newerIDLow)
	require.NoError(t, err)
	require.Len(t, versions, 2, "both observations must be kept when their timestamps tie")

	seen := make(map[string]bool, len(versions))
	var current []string
	for _, version := range versions {
		seen[version.WasmHash] = true
		assert.True(t, version.DecodedAt.Equal(tied), "both versions share the clock's timestamp")
		if version.Current {
			current = append(current, version.WasmHash)
		}
	}
	assert.True(t, seen["hash-v1"] && seen["hash-v2"])
	require.Len(t, current, 1, "exactly one tied version is current")

	got, err := m.GetSpec(ctx, "testnet", newerIDLow, "")
	require.NoError(t, err)
	assert.Equal(t, current[0], got.WasmHash,
		"the version flagged Current must be the interface GetSpec returns")
	assert.Equal(t, "v2", got.Interface.Functions[0].Name,
		"the latest save points the contract at its new interface")
}

// TestPingEdgeCases covers table-driven edge cases for Ping: a context whose
// deadline has already passed, an already-cancelled context, and a deadline
// far enough away that it must not fail the call.
//
// Ping also promises not to change any stored data, so every case seeds both
// halves of the store and checks afterwards that neither moved.
func TestPingEdgeCases(t *testing.T) {
	t.Parallel()

	// A deadline in the past leaves the context already done, which is the
	// boundary between "ask the backing service" and "stop now". Cancelling
	// through t.Cleanup keeps the contexts alive for the parallel subtests
	// below, which start only after this function returns.
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(0, 0))
	t.Cleanup(cancelExpired)
	require.ErrorIs(t, expired.Err(), context.DeadlineExceeded, "the deadline must be in the past")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, cancelled.Err(), context.Canceled, "cancelling must mark the context done")

	// Far enough away that the wall clock cannot overtake it while the
	// test runs, so the case really tests "not yet cancelled".
	future, cancelFuture := context.WithTimeout(context.Background(), time.Hour)
	t.Cleanup(cancelFuture)

	tests := []struct {
		name    string
		ctx     context.Context
		wantErr error
	}{
		{
			name:    "expired deadline reports DeadlineExceeded",
			ctx:     expired,
			wantErr: context.DeadlineExceeded,
		},
		{
			name:    "cancelled context reports Canceled",
			ctx:     cancelled,
			wantErr: context.Canceled,
		},
		{
			name: "future deadline succeeds",
			ctx:  future,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			m := NewMemory()

			require.NoError(t, m.Save(ctx,
				Contract{ContractID: newerIDLow, Network: "testnet", CurrentWasmHash: "hash-ping"},
				newerTestIface("ping")))
			secret, err := m.Subscribe(ctx, "testnet", []string{newerIDLow}, "https://example.com/webhook")
			require.NoError(t, err)

			err = m.Ping(tt.ctx)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			got, err := m.GetContract(ctx, "testnet", newerIDLow)
			require.NoError(t, err, "a ping must not remove a stored contract")
			assert.Equal(t, "hash-ping", got.CurrentWasmHash)

			versions, err := m.ListVersions(ctx, "testnet", newerIDLow)
			require.NoError(t, err)
			assert.Len(t, versions, 1, "a ping must not add or drop a version")

			items, err := m.ListSubscriptions(ctx)
			require.NoError(t, err)
			require.Len(t, items, 1, "a ping must not remove a subscription")
			assert.Equal(t, secret, items[0].ID)
			assert.True(t, items[0].Active, "a ping must not deactivate a subscription")
		})
	}
}

// TestSubscribeEdgeCases covers table-driven edge cases for Subscribe: an
// empty contract list, empty network and URL values, and an already-cancelled
// context.
func TestSubscribeEdgeCases(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	stamp := time.Date(2026, 8, 9, 10, 11, 12, 0, time.UTC)

	tests := []struct {
		name        string
		ctx         context.Context
		network     string
		contractIDs []string
		url         string
		wantErr     error
	}{
		{
			// An empty list is the boundary between "watch these" and
			// "watch nothing". The store records the subscription as given;
			// refusing an empty list is the API's job.
			name:        "empty contract list is stored with zero contracts",
			ctx:         context.Background(),
			network:     "testnet",
			contractIDs: nil,
			url:         "https://example.com/webhook",
		},
		{
			// Subscribe persists what it is given rather than inventing a
			// default for a missing network or URL, so an invalid request can
			// never be mistaken for a valid one later.
			name:        "empty network and url are stored verbatim",
			ctx:         context.Background(),
			network:     "",
			contractIDs: []string{},
			url:         "",
		},
		{
			name:        "cancelled context returns the error and stores nothing",
			ctx:         cancelled,
			network:     "testnet",
			contractIDs: []string{newerIDLow},
			url:         "https://example.com/webhook",
			wantErr:     context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			m := NewMemory()
			m.SetClock(func() time.Time { return stamp })

			secret, err := m.Subscribe(tt.ctx, tt.network, tt.contractIDs, tt.url)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, secret, "a failed Subscribe must not hand back a secret")

				items, listErr := m.ListSubscriptions(ctx)
				require.NoError(t, listErr)
				assert.Empty(t, items, "a cancelled Subscribe must not leave a subscription behind")
				return
			}
			require.NoError(t, err)

			assert.True(t, strings.HasPrefix(secret, "sub_"), "got %q", secret)
			assert.Len(t, secret, len("sub_")+64, "the secret is 256 random bits, not a counter")

			sub, err := m.GetSubscription(ctx, secret)
			require.NoError(t, err)
			assert.Equal(t, tt.network, sub.Network, "the network round-trips verbatim")
			assert.Equal(t, tt.url, sub.URL, "the URL round-trips verbatim")
			assert.Len(t, sub.ContractIDs, len(tt.contractIDs))
			assert.True(t, sub.Active, "a new subscription is active")
			assert.True(t, sub.CreatedAt.Equal(stamp), "the subscription carries the clock's timestamp")

			items, err := m.ListSubscriptions(ctx)
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.Equal(t, secret, items[0].ID)
			assert.Equal(t, len(tt.contractIDs), items[0].Contracts, "the summary counts the stored contracts")
			assert.True(t, items[0].Active)
		})
	}
}

// TestSetClockEdgeCases covers table-driven edge cases for SetClock:
// empty/zero time, boundary values (Unix epoch, far future), and pre-epoch time.
func TestSetClockEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		clock    func() time.Time
		wantTime time.Time
	}{
		{
			name: "zero value time boundary",
			clock: func() time.Time {
				return time.Time{}
			},
			wantTime: time.Time{},
		},
		{
			name: "unix epoch boundary",
			clock: func() time.Time {
				return time.Unix(0, 0).UTC()
			},
			wantTime: time.Unix(0, 0).UTC(),
		},
		{
			name: "far future boundary",
			clock: func() time.Time {
				return time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
			},
			wantTime: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			name: "pre-epoch boundary",
			clock: func() time.Time {
				return time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC)
			},
			wantTime: time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			m := NewMemory()
			m.SetClock(tt.clock)

			contractID := "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			c := Contract{
				ContractID:      contractID,
				Network:         "testnet",
				CurrentWasmHash: "hash-edge-test",
			}
			err := m.Save(ctx, c, newerTestIface("edge_case"))
			require.NoError(t, err)

			got, err := m.GetContract(ctx, "testnet", contractID)
			require.NoError(t, err)
			assert.True(t, got.FirstSeen.Equal(tt.wantTime), "expected FirstSeen to match clock output")
			assert.True(t, got.LastRefreshed.Equal(tt.wantTime), "expected LastRefreshed to match clock output")

			versions, err := m.ListVersions(ctx, "testnet", contractID)
			require.NoError(t, err)
			require.NotEmpty(t, versions)
			assert.True(t, versions[0].DecodedAt.Equal(tt.wantTime), "expected DecodedAt to match clock output")

			subID, err := m.Subscribe(ctx, "testnet", []string{contractID}, "https://example.com/webhook")
			require.NoError(t, err)
			require.NotEmpty(t, subID)

			sub, err := m.GetSubscription(ctx, subID)
			require.NoError(t, err)
			assert.True(t, sub.CreatedAt.Equal(tt.wantTime), "expected subscription CreatedAt to match clock output")
		})
	}
}

// TestMarkSubscriptionInactiveEdgeCases covers table-driven edge cases for MarkSubscriptionInactive:
// empty input, invalid/non-existent secret, and already inactive subscription.
func TestMarkSubscriptionInactiveEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(ctx context.Context, m *Memory) string
		secret     string
		wantErr    error
		wantActive bool
	}{
		{
			name:    "empty secret returns ErrNotFound",
			secret:  "",
			wantErr: ErrNotFound,
		},
		{
			name:    "non-existent secret returns ErrNotFound",
			secret:  "non-existent-subscription-secret",
			wantErr: ErrNotFound,
		},
		{
			name: "already inactive subscription remains inactive",
			setup: func(ctx context.Context, m *Memory) string {
				sec, err := m.Subscribe(ctx, "testnet", []string{newerIDLow}, "https://example.com/webhook")
				require.NoError(t, err)
				err = m.MarkSubscriptionInactive(ctx, sec)
				require.NoError(t, err)
				return sec
			},
			wantErr:    nil,
			wantActive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			m := NewMemory()

			secret := tt.secret
			if tt.setup != nil {
				secret = tt.setup(ctx, m)
			}

			err := m.MarkSubscriptionInactive(ctx, secret)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				sub, err := m.GetSubscription(ctx, secret)
				require.NoError(t, err)
				assert.Equal(t, tt.wantActive, sub.Active)
			}
		})
	}
}

// TestUnsubscribeEdgeCases covers table-driven edge cases for Unsubscribe:
// empty input, invalid/non-existent secret, and already deleted subscription.
func TestUnsubscribeEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(ctx context.Context, m *Memory) string
		secret  string
		wantErr error
	}{
		{
			name:    "empty secret returns ErrNotFound",
			secret:  "",
			wantErr: ErrNotFound,
		},
		{
			name:    "non-existent secret returns ErrNotFound",
			secret:  "non-existent-subscription-secret",
			wantErr: ErrNotFound,
		},
		{
			name: "already unsubscribed secret returns ErrNotFound",
			setup: func(ctx context.Context, m *Memory) string {
				sec, err := m.Subscribe(ctx, "testnet", []string{newerIDLow}, "https://example.com/webhook")
				require.NoError(t, err)
				err = m.Unsubscribe(ctx, sec)
				require.NoError(t, err)
				return sec
			},
			wantErr: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			m := NewMemory()

			secret := tt.secret
			if tt.setup != nil {
				secret = tt.setup(ctx, m)
			}

			err := m.Unsubscribe(ctx, secret)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
