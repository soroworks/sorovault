package store_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault"
	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/store"
)

// TestSymbolsBackfillMatchesSave checks that migration 000002's backfill
// produces exactly what Save writes, so rows stored before the migration are
// searchable the same way as rows stored after it.
//
// It is deliberately not parallel: Go runs sequential tests before resuming
// parallel ones, so this never races the conformance suite's truncations.
func TestSymbolsBackfillMatchesSave(t *testing.T) {
	url := os.Getenv(TestDatabaseURLEnv)
	if url == "" {
		t.Skip(TestDatabaseURLEnv + " not set")
	}
	ctx := context.Background()

	require.NoError(t, store.Migrate(url))
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()

	_, err = pool.Exec(ctx, "TRUNCATE contracts CASCADE")
	require.NoError(t, err)

	rich := iface("zeta")
	rich.Functions = append(rich.Functions, model.Function{Name: "Alpha"}, model.Function{Name: "beta"})
	rich.Types.Structs = []model.Struct{{Name: "beta", Fields: []model.StructField{}}}
	rich.Types.Unions = []model.Union{{Name: "DataKey", Cases: []model.UnionCase{}}}
	rich.Types.Enums = []model.Enum{{Name: "Status", Cases: []model.EnumCase{}}}
	rich.Types.ErrorEnums = []model.ErrorEnum{{Name: "Error", Cases: []model.EnumCase{}}}
	rich.Events = []model.Event{{Name: "Moved", Params: []model.EventParam{}}}

	s := store.NewPostgresFromPool(pool)
	require.NoError(t, s.Save(ctx, contract(idA, "testnet", "h1"), rich))

	var saved string
	require.NoError(t, pool.QueryRow(ctx, "SELECT symbols FROM specs").Scan(&saved))
	assert.Equal(t, strings.Join(rich.SymbolNames(), "\n"), saved)

	// Simulate a row written before the column existed, then replay the
	// migration's backfill statement against it.
	_, err = pool.Exec(ctx, "UPDATE specs SET symbols = ''")
	require.NoError(t, err)

	up, err := sorovault.Migrations.ReadFile("migrations/000002_spec_symbols.up.sql")
	require.NoError(t, err)
	backfill := string(up[strings.Index(string(up), "UPDATE specs"):])
	_, err = pool.Exec(ctx, backfill)
	require.NoError(t, err)

	var backfilled string
	require.NoError(t, pool.QueryRow(ctx, "SELECT symbols FROM specs").Scan(&backfilled))
	assert.Equal(t, saved, backfilled)

	_, err = pool.Exec(ctx, "TRUNCATE contracts CASCADE")
	require.NoError(t, err)
}
