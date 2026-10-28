package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/refresher"
)

// printSweepSummary writes one line describing a refresh sweep.
func printSweepSummary(w io.Writer, sum refresher.Summary) {
	_, _ = fmt.Fprintf(w, "checked %d, changed %d, missing %d, failed %d, busy %d\n",
		sum.Checked, sum.Changed, sum.Missing, sum.Failed, sum.Busy)
}

func newRefreshCmd() *cobra.Command {
	var (
		asJSON   bool
		all      bool
		fromFile string
	)

	cmd := &cobra.Command{
		Use:   "refresh [contract_id]...",
		Short: "Re-check registered contracts for an upgraded interface",
		Long: `Compare each registered contract against the network and record a new
interface version if its WASM has changed.

With --all it runs one sweep over every contract on the network, regardless
of how recently each was refreshed, then exits. It is meant for a cron job or
for catching up after an RPC outage; it does not wait an interval.

Unlike "add", refresh starts from what is already stored, so an unchanged
contract costs a single RPC round trip: the module is only downloaded once
its hash is known to differ.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) > 0 || fromFile != "" {
					return fmt.Errorf("--all refreshes every contract and cannot be combined with contract IDs or --from-file")
				}
				return nil
			}
			if len(args) == 0 && fromFile == "" {
				return fmt.Errorf("at least one contract ID is required, or use --from-file or --all")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			contractIDs := args
			if !all {
				var err error
				contractIDs, err = loadContractIDs(args, fromFile, cmd.InOrStdin())
				if err != nil {
					return err
				}
				if len(contractIDs) == 0 {
					return fmt.Errorf("no contract IDs found")
				}
			}

			if all {
				return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
					interval := a.cfg.RefreshInterval
					if interval <= 0 {
						// SweepAll ignores staleness and pacing; the interval
						// only satisfies New's positive-duration guard.
						interval = time.Minute
					}

					r, err := refresher.New(a.registry, refresher.Options{
						Interval: interval,
						Timeout:  a.cfg.RPCTimeout,
						Logger:   a.log.With("component", "refresher"),
					})
					if err != nil {
						return err
					}

					summary, err := r.SweepAll(ctx)
					if err != nil {
						return err
					}

					if asJSON {
						if err := writeJSON(cmd.OutOrStdout(), summary); err != nil {
							return err
						}
					} else {
						printSweepSummary(cmd.OutOrStdout(), summary)
					}

					if summary.Failed > 0 {
						return fmt.Errorf("%w: %d contract(s) failed to refresh", errPrinted, summary.Failed)
					}
					return nil
				})
			}

			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				var failed int

				for _, contractID := range contractIDs {
					fetchCtx, cancel := a.fetchContext(ctx)
					result, err := a.registry.Refresh(fetchCtx, contractID)
					cancel()

					if err != nil {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", contractID, err)
						failed++
						continue
					}

					if asJSON {
						if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
							return err
						}
						continue
					}

					if result.Changed {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s upgraded to %s\n",
							result.Contract.ContractID, result.Contract.CurrentWasmHash)
						continue
					}
					if result.Missing {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s missing from the network\n",
							result.Contract.ContractID)
						continue
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s unchanged\n", result.Contract.ContractID)
				}

				if failed > 0 {
					return fmt.Errorf("%w: %d of %d contract(s) failed", errPrinted, failed, len(contractIDs))
				}
				return nil
			})
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the full result as JSON")
	cmd.Flags().BoolVar(&all, "all", false, "run one sweep over every contract on the network")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read contract IDs from a file (one per line; use - for stdin)")
	registerContractIDCompletion(cmd, "all")
	return cmd
}
