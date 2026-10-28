package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

// doctorCheck is one thing a doctor run checks.
type doctorCheck struct {
	// Name identifies the check in the report.
	Name string
	// Run performs the check. It returns a line describing what it found,
	// or an error if the check failed.
	Run func(ctx context.Context) (string, error)
}

// doctorResult is the outcome of one check.
type doctorResult struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// Detail describes what the check found.
	Detail string `json:"detail,omitempty"`
	// Error says why the check failed.
	Error string `json:"error,omitempty"`
}

// doctorReport is the outcome of a whole doctor run.
type doctorReport struct {
	OK     bool           `json:"ok"`
	Checks []doctorResult `json:"checks"`
}

// failed counts the checks that did not pass.
func (r doctorReport) failed() int {
	n := 0
	for _, c := range r.Checks {
		if !c.OK {
			n++
		}
	}
	return n
}

// doctorDeps are the things a doctor run talks to. They are injected so the
// command can be exercised without a database or a network.
type doctorDeps struct {
	// Ping checks that the database answers.
	Ping func(ctx context.Context) error
	// Migrations reports the migration version the database has applied,
	// the latest embedded one, and whether the database is marked dirty.
	Migrations func(ctx context.Context) (current, latest uint, dirty bool, err error)
	// Passphrase reads the passphrase the RPC endpoint serves.
	Passphrase func(ctx context.Context) (string, error)
	// ExpectPassphrase is the network the operator configured.
	ExpectPassphrase string
	// Close releases whatever the dependencies hold.
	Close func() error
}

// newDoctorCmd builds the doctor command. Its dependencies are injected so
// the command can be exercised without a database or a network.
func newDoctorCmd(depsFor func() (doctorDeps, error)) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that a deployment is healthy",
		Long: `Check the things that are wrong most often: whether the database
answers, whether its schema is up to date, whether the RPC endpoint is
reachable, and whether it serves the network this instance expects.

Every check is read-only. Nothing is written to the database, no migration
is applied, and nothing is changed on the network.`,
		Args: cobra.NoArgs,
		// The report already says which checks failed, so the error only
		// carries the count and cobra need not print it as well.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			deps, err := depsFor()
			if err != nil {
				return err
			}
			if deps.Close != nil {
				defer func() { _ = deps.Close() }()
			}

			report := runDoctor(cmd.Context(), doctorChecks(deps))

			if asJSON {
				if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else {
				printDoctor(cmd.OutOrStdout(), report)
			}

			if !report.OK {
				return fmt.Errorf("%d of %d checks failed", report.failed(), len(report.Checks))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON")
	return cmd
}

// doctorDepsFromEnv builds the doctor's dependencies from the configuration
// source (the --config file where named, then the environment, as with
// every other command).
func doctorDepsFromEnv() (doctorDeps, error) {
	cfg, err := config.LoadWith(configPath)
	if err != nil {
		return doctorDeps{}, err
	}
	return newDoctorDeps(cfg)
}

// newDoctorDeps builds the doctor's dependencies from the configuration:
// the database at DATABASE_URL and the RPC endpoint at RPC_URL.
//
// The passphrase is read once and remembered, so the reachability and the
// passphrase check cost one round trip between them.
func newDoctorDeps(cfg *config.Config) (doctorDeps, error) {
	fetcher := stellar.NewRPCFetcher(cfg.RPCURL, nil)

	var (
		once       sync.Once
		passphrase string
		passErr    error
	)
	readPassphrase := func(ctx context.Context) (string, error) {
		once.Do(func() { passphrase, passErr = fetcher.Network(ctx) })
		return passphrase, passErr
	}

	return doctorDeps{
		Ping: func(ctx context.Context) error {
			return store.Ping(ctx, cfg.DatabaseURL)
		},
		Migrations: func(ctx context.Context) (uint, uint, bool, error) {
			return store.MigrationStatus(ctx, cfg.DatabaseURL)
		},
		Passphrase:       readPassphrase,
		ExpectPassphrase: cfg.NetworkPassphrase,
		Close:            fetcher.Close,
	}, nil
}

// doctorChecks builds the checks a doctor run performs, in the order they
// are reported.
func doctorChecks(deps doctorDeps) []doctorCheck {
	return []doctorCheck{
		{
			Name: "database",
			Run: func(ctx context.Context) (string, error) {
				if err := deps.Ping(ctx); err != nil {
					return "", err
				}
				return "the database answers", nil
			},
		},
		{
			Name: "migrations",
			Run: func(ctx context.Context) (string, error) {
				current, latest, dirty, err := deps.Migrations(ctx)
				if err != nil {
					return "", err
				}
				if dirty {
					return "", fmt.Errorf("the database is at version %d but marked dirty; a previous migration failed partway and needs manual repair", current)
				}
				if current < latest {
					return "", fmt.Errorf("the database is at version %d, but version %d is available; run `sorovault migrate`", current, latest)
				}
				return fmt.Sprintf("the database is at version %d, the latest is %d", current, latest), nil
			},
		},
		{
			Name: "rpc",
			Run: func(ctx context.Context) (string, error) {
				passphrase, err := deps.Passphrase(ctx)
				if err != nil {
					return "", err
				}
				return "the endpoint is reachable, serving " + stellar.NetworkName(passphrase), nil
			},
		},
		{
			Name: "passphrase",
			Run: func(ctx context.Context) (string, error) {
				passphrase, err := deps.Passphrase(ctx)
				if err != nil {
					return "", err
				}
				if err := registry.CheckPassphrase(passphrase, deps.ExpectPassphrase); err != nil {
					return "", err
				}
				return "the endpoint serves the configured network", nil
			},
		},
	}
}

// runDoctor runs every check and collects the results. A check that fails
// does not stop the ones after it: the point of a doctor run is to say
// everything that is wrong, not just the first thing.
func runDoctor(ctx context.Context, checks []doctorCheck) doctorReport {
	report := doctorReport{OK: true, Checks: make([]doctorResult, 0, len(checks))}

	for _, check := range checks {
		detail, err := check.Run(ctx)
		result := doctorResult{Name: check.Name, OK: err == nil, Detail: detail}
		if err != nil {
			result.Error = err.Error()
			report.OK = false
		}
		report.Checks = append(report.Checks, result)
	}

	return report
}

// printDoctor writes the report as one line per check.
func printDoctor(w io.Writer, report doctorReport) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range report.Checks {
		status, detail := "PASS", c.Detail
		if !c.OK {
			status, detail = "FAIL", c.Error
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", status, c.Name, detail)
	}
	_ = tw.Flush()
}
