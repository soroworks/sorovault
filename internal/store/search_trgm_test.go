package store_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/store"
)

var trgmIndexes = []string{"contracts_contract_id_trgm", "contracts_name_trgm", "specs_symbols_trgm"}

// TestSearchTrgmMigration proves migration 19 adds the trigram indexes, that
// its down migration removes them and leaves pg_trgm installed, and that
// migrating up again puts them back.
func TestSearchTrgmMigration(t *testing.T) {
	baseURL := os.Getenv(TestDatabaseURLEnv)
	if baseURL == "" {
		t.Skip(TestDatabaseURLEnv + " not set")
	}
	ctx := context.Background()
	dbURL := isolatedDatabase(t, ctx, baseURL)

	require.NoError(t, store.Migrate(dbURL))
	assert.Equal(t, trgmIndexes, trgmIndexNames(t, ctx, dbURL))

	require.NoError(t, store.MigrateDown(dbURL))
	assert.Empty(t, trgmIndexNames(t, ctx, dbURL))
	assert.True(t, extensionInstalled(t, ctx, dbURL, "pg_trgm"),
		"the down migration leaves pg_trgm installed")

	require.NoError(t, store.Migrate(dbURL))
	assert.Equal(t, trgmIndexes, trgmIndexNames(t, ctx, dbURL))
}

// TestSearchTrgmMigrationWithoutPrivilege proves a role that may not create
// pg_trgm still migrates to the latest version, without the indexes, and
// that search gives the same answers without them.
func TestSearchTrgmMigrationWithoutPrivilege(t *testing.T) {
	baseURL := os.Getenv(TestDatabaseURLEnv)
	if baseURL == "" {
		t.Skip(TestDatabaseURLEnv + " not set")
	}
	ctx := context.Background()
	dbURL := isolatedDatabase(t, ctx, baseURL)

	// A role that owns the public schema, so it can create the tables, but
	// lacks CREATE on the database, which installing a trusted extension
	// needs. That is the shape of an application role on a locked-down
	// managed service.
	admin, err := pgx.Connect(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	role := fmt.Sprintf("sorovault_app_%d", time.Now().UnixNano())
	ident := pgx.Identifier{role}.Sanitize()
	dbName := pgx.Identifier{dbNameOf(t, dbURL)}.Sanitize()
	for _, stmt := range []string{
		"CREATE ROLE " + ident + " LOGIN PASSWORD 'app'",
		"REVOKE CREATE ON DATABASE " + dbName + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + dbName + " TO " + ident,
		"ALTER SCHEMA public OWNER TO " + ident,
	} {
		_, err := admin.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	t.Cleanup(func() {
		// Cleanups run last first, so the isolated database still exists
		// here. The role owns its public schema and holds a grant on it, and
		// both must go before the role can be dropped from the base database.
		bg := context.Background()
		_, _ = admin.Exec(bg, "DROP OWNED BY "+ident+" CASCADE")
		conn, err := pgx.Connect(bg, baseURL)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(bg) }()
		_, _ = conn.Exec(bg, "DROP ROLE IF EXISTS "+ident)
	})

	u, err := url.Parse(dbURL)
	require.NoError(t, err)
	u.User = url.UserPassword(role, "app")
	appURL := u.String()

	require.NoError(t, store.Migrate(appURL), "a missing privilege must not fail the migration")
	current, latest, dirty, err := store.MigrationStatus(ctx, appURL)
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, latest, current)
	assert.False(t, extensionInstalled(t, ctx, dbURL, "pg_trgm"))
	assert.Empty(t, trgmIndexNames(t, ctx, dbURL))

	pool, err := pgxpool.New(ctx, appURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	assertSearchResults(t, store.NewPostgresFromPool(pool))
}

// TestSearchWithTrigramIndexes runs searches with sequential scans turned
// off, so Postgres answers from the trigram and docs indexes wherever it can,
// including the short queries trigrams cannot narrow. The answers must match
// what a sequential scan gives.
func TestSearchWithTrigramIndexes(t *testing.T) {
	baseURL := os.Getenv(TestDatabaseURLEnv)
	if baseURL == "" {
		t.Skip(TestDatabaseURLEnv + " not set")
	}
	ctx := context.Background()
	dbURL := isolatedDatabase(t, ctx, baseURL)
	require.NoError(t, store.Migrate(dbURL))
	require.Equal(t, trgmIndexes, trgmIndexNames(t, ctx, dbURL))

	cfg, err := pgxpool.ParseConfig(dbURL)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["enable_seqscan"] = "off"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	assertSearchResults(t, store.NewPostgresFromPool(pool))
}

// assertSearchResults saves a few contracts and checks the IDs and totals
// for queries that match through each searched field, alone and together.
func assertSearchResults(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()

	withdraw := iface("withdraw_rewards")
	withdraw.Functions[0].Doc = "Pays out accrued staking rewards."
	require.NoError(t, s.Save(ctx, contract(idA, "testnet", "h1"), withdraw))
	require.NoError(t, s.Save(ctx, contract(idB, "testnet", "h2"), iface("burn")))
	require.NoError(t, s.SetName(ctx, "testnet", idB, "rewards vault 99"))
	// An old spec of idB named a symbol the current one does not.
	require.NoError(t, s.Save(ctx, contract(idB, "mainnet", "old"), iface("legacy_swap")))
	require.NoError(t, s.Save(ctx, contract(idB, "mainnet", "new"), iface("swap")))

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"withdraw", []string{idA}},                       // symbol
		{"rewards", []string{idA, idB}},                   // symbol of one, name of the other
		{"staking", []string{idA}},                        // docs only
		{"accrued staking", []string{idA}},                // docs, every term
		{"accrued nowhere", []string{}},                   // docs, a term missing
		{"vault", []string{idB}},                          // name
		{"cdzzxvgu", []string{idA}},                       // ID
		{"legacy", []string{}},                            // old spec only
		{"swap", []string{idB}},                           // current spec on mainnet
		{"9", []string{idB}},                              // one character
		{"99", []string{idB}},                             // two characters
		{"cd", []string{idA, idB, idB}},                   // every row, both networks
		{"zzz", []string{idB, idB}},                       // ID, three characters
		{"_", []string{idA}},                              // literal underscore
		{"%", []string{}},                                 // literal percent
		{"w", []string{idA, idB, idB}},                    // a bit of everything
		{"pays out accrued staking", []string{idA}},       // long docs query
		{"rewards vault 99", []string{idB}},               // whole name
		{"CDZZZPYQALFRBQAZD5KCW2SZK", []string{idB, idB}}, // long ID prefix
	} {
		page, err := s.ListContracts(ctx, store.ListFilter{Query: tc.query, Limit: 100})
		require.NoError(t, err, tc.query)
		got := pageIDs(page)
		sort.Strings(got)
		want := append([]string{}, tc.want...)
		sort.Strings(want)
		assert.Equal(t, want, got, "query %q", tc.query)
		assert.Equal(t, len(tc.want), page.Total, "query %q", tc.query)
	}
}

func trgmIndexNames(t *testing.T, ctx context.Context, dbURL string) []string {
	t.Helper()
	conn, err := pgx.Connect(ctx, dbURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `SELECT indexname FROM pg_indexes WHERE indexname LIKE '%\_trgm' ORDER BY indexname`)
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return names
}

func extensionInstalled(t *testing.T, ctx context.Context, dbURL, name string) bool {
	t.Helper()
	conn, err := pgx.Connect(ctx, dbURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	var installed bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`, name).Scan(&installed))
	return installed
}

func dbNameOf(t *testing.T, dbURL string) string {
	t.Helper()
	u, err := url.Parse(dbURL)
	require.NoError(t, err)
	return u.Path[1:]
}
