package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/store"
)

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Long: `Apply pending database migrations.

Run "sorovault migrate status" to see what is applied and what is pending
without applying anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			databaseURL, err := requireDatabaseURL()
			if err != nil {
				return err
			}

			if err := store.Migrate(databaseURL); err != nil {
				return err
			}

			version, dirty, err := store.MigrationVersion(databaseURL)
			if err != nil {
				return err
			}
			if dirty {
				return fmt.Errorf("database is at version %d but marked dirty; a previous migration failed partway and needs manual repair", version)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "database is at migration version %d\n", version)
			return nil
		},
	}

	cmd.AddCommand(newMigrateStatusCmd(migrateStatusDepsFromEnv))
	return cmd
}

// migrateStatusDeps are the things a migration status run talks to. They are
// injected so the command can be exercised without a database.
type migrateStatusDeps struct {
	// Version reports the migration version the database has applied, and
	// whether the database is marked dirty.
	Version func(ctx context.Context) (version uint, dirty bool, err error)
	// Migrations lists the migrations embedded in the binary, oldest first.
	Migrations func(ctx context.Context) ([]store.Migration, error)
}

// migrateStatusDepsFromEnv builds the status command's dependencies from the
// environment.
func migrateStatusDepsFromEnv() (migrateStatusDeps, error) {
	databaseURL, err := requireDatabaseURL()
	if err != nil {
		return migrateStatusDeps{}, err
	}

	return migrateStatusDeps{
		// golang-migrate is the same source of truth the migrate command
		// applies, so the two cannot disagree about where the database is.
		// Opening a database through it creates the version table if the
		// database has none, which is the one thing a status run may write.
		Version: func(context.Context) (uint, bool, error) {
			return store.MigrationVersion(databaseURL)
		},
		Migrations: func(context.Context) ([]store.Migration, error) {
			return store.EmbeddedMigrations()
		},
	}, nil
}

// migrateStatusReport is what a status run found, in the form the --json
// flag emits.
type migrateStatusReport struct {
	// Current is the migration version the database has applied.
	Current uint `json:"current_version"`
	// Latest is the highest migration version embedded in this binary.
	Latest uint `json:"latest_version"`
	// Dirty says whether the database is marked dirty.
	Dirty bool `json:"dirty"`
	// Pending lists the migrations the database has not applied, oldest
	// first. It is empty rather than null when there are none.
	Pending []migrationStatusItem `json:"pending"`
	// Message says what the report means and what to do about it, so a
	// reader of the JSON is told what a reader of the text report is.
	Message string `json:"message"`
}

// migrationStatusItem is one migration in a status report.
type migrationStatusItem struct {
	// Version is the migration's version number.
	Version uint `json:"version"`
	// Name is the migration's name.
	Name string `json:"name"`
}

// The wording of the report. The text form and the JSON form both come
// from here, so they cannot describe the same state differently.
const (
	// dirtyState says what a dirty database means.
	dirtyState = "the database is marked dirty: a migration failed partway and the schema is in an unknown state"
	// dirtyAdvice says what to do about a dirty database.
	dirtyAdvice = "the pending migrations cannot be applied until the schema is repaired: " +
		"fix the schema by hand, then set the version with the golang-migrate CLI " +
		"(`migrate -path migrations -database \"$DATABASE_URL\" force <version>`) before applying migrations again"
	// upToDate says the database has every migration this binary carries.
	upToDate = "the database is up to date"
)

// statusMessage summarises the report in one line: what state the database
// is in, and what to do about it.
//
// The text report says the same in more than one line where a terminal
// reads better.
func statusMessage(report migrateStatusReport) string {
	switch {
	case report.Dirty:
		return dirtyState + "; " + dirtyAdvice

	case len(report.Pending) > 0:
		return fmt.Sprintf("%d migration(s) pending; run `sorovault migrate` to apply them", len(report.Pending))

	case report.Current > report.Latest:
		return fmt.Sprintf("the database is at version %d, ahead of the migrations in this binary (version %d)",
			report.Current, report.Latest)

	default:
		return upToDate
	}
}

// newMigrateStatusCmd builds the "migrate status" command. Its dependencies
// are injected so it can be exercised without a database.
func newMigrateStatusCmd(depsFor func() (migrateStatusDeps, error)) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show which migrations are applied and which are pending",
		Long: `Report the migration version the database has applied, the latest
version embedded in this binary, and the migrations that have not been
applied yet.

No migration is applied: a status run reports and stops there.

The command exits non-zero when migrations are pending, so it can gate a
deployment.`,
		Args: cobra.NoArgs,
		// The report already says what is wrong, so the error only carries
		// the summary, and main prints that rather than cobra printing it
		// a second time.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := depsFor()
			if err != nil {
				return err
			}
			return reportMigrationStatus(cmd.Context(), cmd.OutOrStdout(), deps, asJSON)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON")
	return cmd
}

// reportMigrationStatus works out where the database is, writes the report,
// and fails when the database is not in a state a deployment can start from.
func reportMigrationStatus(ctx context.Context, w io.Writer, deps migrateStatusDeps, asJSON bool) error {
	current, dirty, err := deps.Version(ctx)
	if err != nil {
		return err
	}

	migrations, err := deps.Migrations(ctx)
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		// A binary that embeds no migrations cannot say whether a database
		// is up to date, and saying "up to date" would be a lie.
		return errors.New("this binary embeds no migrations")
	}

	report := migrateStatusReport{
		Current: current,
		Dirty:   dirty,
		Pending: []migrationStatusItem{},
	}
	for _, m := range migrations {
		if m.Version > report.Latest {
			report.Latest = m.Version
		}
		if m.Version > current {
			report.Pending = append(report.Pending, migrationStatusItem{
				Version: m.Version,
				Name:    m.Name,
			})
		}
	}
	report.Message = statusMessage(report)

	if asJSON {
		if err := writeJSON(w, report); err != nil {
			return err
		}
	} else if err := writeMigrationStatus(w, report); err != nil {
		return err
	}

	switch {
	case report.Dirty:
		// A dirty database is the more urgent problem: the schema is in
		// an unknown state, so the pending list cannot be trusted either.
		return errors.New("the database is marked dirty and needs manual repair")

	case len(report.Pending) > 0:
		return errors.New(report.Message)

	default:
		// Nothing is pending, whether the database is at the latest version
		// or ahead of it; the report says which of the two it is.
		return nil
	}
}

// writeMigrationStatus writes the human-readable report.
func writeMigrationStatus(w io.Writer, report migrateStatusReport) error {
	_, _ = fmt.Fprintf(w, "current version: %d\n", report.Current)
	_, _ = fmt.Fprintf(w, "latest version:  %d\n", report.Latest)

	switch {
	case report.Dirty:
		_, _ = fmt.Fprintf(w, "\n%s\n", dirtyState)
		_, _ = fmt.Fprintf(w, "%s\n", dirtyAdvice)
		return nil

	case len(report.Pending) > 0:
		_, _ = fmt.Fprintf(w, "\npending migrations:\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  VERSION\tNAME")
		for _, m := range report.Pending {
			_, _ = fmt.Fprintf(tw, "  %d\t%s\n", m.Version, m.Name)
		}
		return tw.Flush()

	case report.Current > report.Latest:
		_, _ = fmt.Fprintf(w, "\n%s\n", report.Message)
		return nil

	default:
		_, _ = fmt.Fprintf(w, "\n%s\n", upToDate)
		return nil
	}
}
