package store

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for golang-migrate

	sorovault "github.com/soroworks/sorovault"
)

// Migrate applies every pending migration to the database at databaseURL.
// It is safe to run against an already-current database, and safe to run
// concurrently: golang-migrate takes an advisory lock for the duration.
func Migrate(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closeMigrator(m)

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("store: applying migrations: %w", err)
	}
	return nil
}

// MigrateDown rolls back every migration, dropping the registry's tables.
// It exists for tests and local resets; it is not wired to a CLI command.
func MigrateDown(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closeMigrator(m)

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("store: reverting migrations: %w", err)
	}
	return nil
}

// MigrationVersion reports the applied version and whether the database is
// in a dirty state, meaning a previous migration failed partway and needs
// manual attention.
func MigrationVersion(databaseURL string) (version uint, dirty bool, err error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer closeMigrator(m)

	version, dirty, err = m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: reading migration version: %w", err)
	}
	return version, dirty, nil
}

func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(sorovault.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: reading embedded migrations: %w", err)
	}

	// The pgx stdlib driver registers itself as "pgx5"; golang-migrate's
	// postgres backend opens the URL through database/sql, and only
	// recognises a postgres:// or postgresql:// scheme.
	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: preparing migrations: %w", err)
	}
	return m, nil
}

// closeMigrator releases a migrator's source and database handles. Errors
// here cannot be acted on — the migration itself has already succeeded or
// failed — so they are deliberately dropped.
func closeMigrator(m *migrate.Migrate) {
	sourceErr, dbErr := m.Close()
	_, _ = sourceErr, dbErr
}

// ensure the postgres driver is linked in; golang-migrate resolves it by URL
// scheme at runtime rather than through a direct reference.
var _ = postgres.Postgres{}
