package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMigrationRoundtrip migrates up fully, down to zero, and up again,
// asserting the schema matches the first up migration.
// It runs when TEST_DATABASE_URL is set, like the other store tests.
func TestMigrationRoundtrip(t *testing.T) {
	url := os.Getenv(TestDatabaseURLEnv)
	if url == "" {
		t.Skip(TestDatabaseURLEnv + " not set")
	}

	ctx := context.Background()

	// First migration up
	require.NoError(t, store.Migrate(url))

	// Migrate down to zero
	require.NoError(t, store.MigrateDown(url))

	// Verify schema is back to zero
	current, latest, dirty, err := store.MigrationStatus(ctx, url)
	require.NoError(t, err)
	assert.False(t, dirty, "database should be at version zero after down migration")
	assert.Equal(t, uint(0), current, "database version should be zero")

	// Migrate up again
	require.NoError(t, store.Migrate(url))

	// Verify schema matches the first up migration
	current, latest, dirty, err = store.MigrationStatus(ctx, url)
	require.NoError(t, err)
	assert.False(t, dirty, "database should not be dirty after second up migration")
	assert.Equal(t, latest, current, "database should be at latest migration version")

	// Verify columns that existed after the first up migration are present
	// after the second up migration.
	checkColumnAfterRoundtrip(t, ctx, url, "specs", "symbols")
	checkColumnAfterRoundtrip(t, ctx, url, "contracts", "source_url")
	checkColumnAfterRoundtrip(t, ctx, url, "contracts", "source_commit")
}

func checkColumnAfterRoundtrip(t *testing.T, ctx context.Context, url, table, column string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()

	var columnName string
	err = pool.QueryRow(context.Background(),
		`SELECT column_name FROM information_schema.columns WHERE table_name = $1 AND table_schema = 'public' AND column_name = $2`,
		table, column).Scan(&columnName)
	assert.NoError(t, err, "column %s.%s should exist after round-trip", table, column)
}