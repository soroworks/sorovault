package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/store"
)

const listBenchmarkContracts = 10_000

type listBenchmarkBackend struct {
	name    string
	network string
	store   store.Store
}

type listBenchmarkQuery struct {
	name  string
	query string
	want  int
}

func BenchmarkListContracts(b *testing.B) {
	backends := listBenchmarkBackends(b)
	queries := []listBenchmarkQuery{
		{name: "many", query: "transfer", want: listBenchmarkContracts},
		{name: "few", query: "reconcile_rare", want: listBenchmarkContracts / 100},
		{name: "none", query: "symbol_not_present", want: 0},
	}

	for _, backend := range backends {
		b.Run(backend.name, func(b *testing.B) {
			for _, query := range queries {
				b.Run(query.name, func(b *testing.B) {
					filter := store.ListFilter{Network: backend.network, Query: query.query}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						page, err := backend.store.ListContracts(context.Background(), filter)
						if err != nil {
							b.Fatal(err)
						}
						if page.Total != query.want {
							b.Fatalf("query %q matched %d contracts, want %d", query.query, page.Total, query.want)
						}
					}
				})
			}
		})
	}
}

func listBenchmarkBackends(b *testing.B) []listBenchmarkBackend {
	b.Helper()
	b.StopTimer()

	ctx := context.Background()
	network := fmt.Sprintf("bench-store-search-%d", os.Getpid())
	memory := store.NewMemory()
	if err := seedListBenchmarkStore(ctx, memory, network); err != nil {
		b.Fatal(err)
	}
	backends := []listBenchmarkBackend{{name: "memory", network: network, store: memory}}

	if url := os.Getenv(TestDatabaseURLEnv); url != "" {
		if err := store.Migrate(url); err != nil {
			b.Fatal(err)
		}
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			b.Fatal(err)
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			b.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM contracts WHERE network = $1", network); err != nil {
			pool.Close()
			b.Fatal(err)
		}
		postgres := store.NewPostgresFromPool(pool)
		if err := seedListBenchmarkStore(ctx, postgres, network); err != nil {
			pool.Close()
			b.Fatal(err)
		}
		// Give the planner statistics for the rows just written, as
		// autovacuum would on a live registry. Without them it plans for
		// the near-empty tables it last saw, and the timings say more about
		// stale statistics than about the query.
		if _, err := pool.Exec(ctx, "ANALYZE contracts, specs"); err != nil {
			pool.Close()
			b.Fatal(err)
		}
		b.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), "DELETE FROM contracts WHERE network = $1", network)
			pool.Close()
		})
		backends = append(backends, listBenchmarkBackend{name: "postgres", network: network, store: postgres})
	}

	b.StartTimer()
	return backends
}

func seedListBenchmarkStore(ctx context.Context, backend store.Store, network string) error {
	for i := 0; i < listBenchmarkContracts; i++ {
		functions := []model.Function{
			{Name: "transfer"},
			{Name: "get_balance"},
			{Name: "withdraw"},
		}
		if i%100 == 0 {
			functions = append(functions, model.Function{Name: "reconcile_rare"})
		}
		iface := &model.Interface{
			ABIVersion: model.ABIVersion,
			Functions:  functions,
			Types: model.Types{
				Structs:    []model.Struct{{Name: "VaultState"}},
				Unions:     []model.Union{},
				Enums:      []model.Enum{{Name: "AssetKind"}},
				ErrorEnums: []model.ErrorEnum{},
			},
			Events: []model.Event{{Name: "TransferEvent"}},
		}
		contract := store.Contract{
			ContractID:      fmt.Sprintf("bench-contract-%05d", i),
			Network:         network,
			CurrentWasmHash: fmt.Sprintf("bench-wasm-%05d", i),
			Name:            fmt.Sprintf("Vault %05d", i),
		}
		if err := backend.Save(ctx, contract, iface); err != nil {
			return fmt.Errorf("seeding contract %d: %w", i, err)
		}
	}
	return nil
}

// searchScaleContracts is the size the trigram indexes are for. At ten
// thousand rows a sequential scan is cheap and the planner rightly prefers it.
const searchScaleContracts = 100_000

// BenchmarkSearchAtScale compares search with and without migration 19's
// trigram indexes over 100,000 contracts, written straight into an isolated
// database with SQL because saving them one at a time would take minutes.
// It needs TEST_DATABASE_URL:
//
//	TEST_DATABASE_URL=... go test -run '^$' -bench SearchAtScale ./internal/store/
func BenchmarkSearchAtScale(b *testing.B) {
	baseURL := os.Getenv(TestDatabaseURLEnv)
	if baseURL == "" {
		b.Skip(TestDatabaseURLEnv + " not set")
	}
	ctx := context.Background()
	dbURL := isolatedDatabase(b, ctx, baseURL)
	if err := store.Migrate(dbURL); err != nil {
		b.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)
	if err := seedSearchScale(ctx, pool); err != nil {
		b.Fatal(err)
	}
	s := store.NewPostgresFromPool(pool)

	queries := []listBenchmarkQuery{
		{name: "rare-symbol", query: "withdraw_rewards", want: 20},
		{name: "id-fragment", query: "CBENCH0004242", want: 1},
		{name: "name-fragment", query: "pool-99999", want: 1},
		{name: "no-match", query: "symbol_not_present", want: 0},
		{name: "every-row", query: "transfer", want: searchScaleContracts},
		{name: "two-chars", query: "zq", want: 0},
		{name: "no-query", query: "", want: searchScaleContracts},
	}
	run := func(b *testing.B) {
		for _, q := range queries {
			b.Run(q.name, func(b *testing.B) {
				filter := store.ListFilter{Query: q.query}
				for i := 0; i < b.N; i++ {
					page, err := s.ListContracts(ctx, filter)
					if err != nil {
						b.Fatal(err)
					}
					if page.Total != q.want {
						b.Fatalf("query %q matched %d contracts, want %d", q.query, page.Total, q.want)
					}
				}
			})
		}
	}

	b.Run("trgm", run)

	// The same searches once the indexes are gone, as on a server where the
	// migration could not install pg_trgm.
	if _, err := pool.Exec(ctx, `
		DROP INDEX contracts_contract_id_trgm, contracts_name_trgm, specs_symbols_trgm;
		ANALYZE contracts, specs`); err != nil {
		b.Fatal(err)
	}
	pool.Reset() // forget statements planned while the indexes existed
	b.Run("no-trgm", run)
}

// seedSearchScale writes searchScaleContracts contracts, each with its current
// spec, an older spec for about one in five, and a doc comment. Every spec
// names transfer; twenty current specs also name withdraw_rewards.
func seedSearchScale(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO contracts (network, contract_id, current_wasm_hash, name, last_refreshed)
		SELECT CASE WHEN g %% 4 = 0 THEN 'testnet' ELSE 'mainnet' END,
		       'CBENCH' || lpad(g::text, 7, '0') || upper(substr(md5(g::text), 1, 43)),
		       md5('w' || g),
		       CASE WHEN g %% 3 = 0 THEN 'pool-' || g ELSE '' END,
		       now() - g * interval '1 second'
		FROM generate_series(1, %[1]d) AS g;

		INSERT INTO specs (network, contract_id, wasm_hash, spec, symbols, docs_tsv)
		SELECT c.network, c.contract_id, w.hash, '{}'::jsonb,
		       'balance' || E'\n' || 'fn_' || substr(md5(w.hash), 1, 10) || E'\n' || 'transfer',
		       to_tsvector('simple', 'returns the balance of ' || substr(md5(c.contract_id), 1, 6))
		FROM contracts c
		CROSS JOIN LATERAL (
		    SELECT c.current_wasm_hash AS hash
		    UNION ALL
		    SELECT md5('old' || c.contract_id) WHERE right(c.contract_id, 1) IN ('0', '1', '2')
		) AS w;

		UPDATE specs s SET symbols = s.symbols || E'\n' || 'withdraw_rewards'
		FROM (SELECT network, contract_id, current_wasm_hash FROM contracts ORDER BY contract_id LIMIT 20) c
		WHERE s.network = c.network AND s.contract_id = c.contract_id AND s.wasm_hash = c.current_wasm_hash;

		ANALYZE contracts, specs`, searchScaleContracts))
	return err
}

// BenchmarkMigrationVersion measures the cost of reading the applied
// migration version from the database with golang-migrate.
// It needs TEST_DATABASE_URL:
//
//	TEST_DATABASE_URL=... go test -run '^$' -bench BenchmarkMigrationVersion ./internal/store/
func BenchmarkMigrationVersion(b *testing.B) {
	url := os.Getenv(TestDatabaseURLEnv)
	if url == "" {
		b.Skip(TestDatabaseURLEnv + " not set")
	}

	if err := store.Migrate(url); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, dirty, err := store.MigrationVersion(url)
		if err != nil {
			b.Fatal(err)
		}
		if dirty {
			b.Fatal("unexpected dirty migration state")
		}
	}
}

// BenchmarkMigrateDown measures the cost of rolling back migrations
// to version zero with golang-migrate.
// It needs TEST_DATABASE_URL:
//
//	TEST_DATABASE_URL=... go test -run '^$' -bench BenchmarkMigrateDown ./internal/store/
func BenchmarkMigrateDown(b *testing.B) {
	url := os.Getenv(TestDatabaseURLEnv)
	if url == "" {
		b.Skip(TestDatabaseURLEnv + " not set")
	}

	if err := store.Migrate(url); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.MigrateDown(url); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMigrate measures the cost of applying migrations
// with golang-migrate.
// It needs TEST_DATABASE_URL:
//
//	TEST_DATABASE_URL=... go test -run '^$' -bench BenchmarkMigrate ./internal/store/
func BenchmarkMigrate(b *testing.B) {
	url := os.Getenv(TestDatabaseURLEnv)
	if url == "" {
		b.Skip(TestDatabaseURLEnv + " not set")
	}

	if err := store.MigrateDown(url); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_ = store.Migrate(url)
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Migrate(url); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := store.MigrateDown(url); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}
