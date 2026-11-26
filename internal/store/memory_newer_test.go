package store

import (
	"context"
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
