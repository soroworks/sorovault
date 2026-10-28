package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/registry"
)

func newAddCmd() *cobra.Command {
	var (
		asJSON   bool
		fromFile string
	)

	cmd := &cobra.Command{
		Use:   "add <contract_id>...",
		Short: "Fetch, decode and register one or more contracts",
		Long: `Fetch each contract's WASM from the network, decode the interface
embedded in it, and store the result.

Adding a contract that is already registered is not an error: it re-checks
the contract against the network and records a new interface version if the
contract has been upgraded since it was last seen.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && fromFile == "" {
				return fmt.Errorf("at least one contract ID is required, or use --from-file")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			contractIDs, err := loadContractIDs(args, fromFile, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if len(contractIDs) == 0 {
				return fmt.Errorf("no contract IDs found")
			}

			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				var failed int

				for _, contractID := range contractIDs {
					fetchCtx, cancel := a.fetchContext(ctx)
					result, err := a.registry.Register(fetchCtx, contractID)
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
					printRegisterResult(cmd.OutOrStdout(), result)
				}

				if failed > 0 {
					return fmt.Errorf("%w: %d of %d contract(s) failed", errPrinted, failed, len(contractIDs))
				}
				return nil
			})
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the full result as JSON")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read contract IDs from a file (one per line; use - for stdin)")
	return cmd
}

// printRegisterResult writes the human-readable summary of an add.
func printRegisterResult(w io.Writer, r *registry.Result) {
	verb := "already current"
	switch {
	case r.Missing:
		verb = "missing from the network"
	case r.Created:
		verb = "registered"
	case r.Changed:
		verb = "upgraded"
	}

	_, _ = fmt.Fprintf(w, "%s %s on %s\n", r.Contract.ContractID, verb, r.Contract.Network)
	_, _ = fmt.Fprintf(w, "  wasm hash  %s\n", r.Contract.CurrentWasmHash)
	printOrigin(w, r.Contract)

	if r.Interface != nil {
		_, _ = fmt.Fprintf(w, "  functions  %d\n", len(r.Interface.Functions))
		if n := len(r.Interface.Types.Structs) + len(r.Interface.Types.Unions) +
			len(r.Interface.Types.Enums) + len(r.Interface.Types.ErrorEnums); n > 0 {
			_, _ = fmt.Fprintf(w, "  types      %d\n", n)
		}
		if n := len(r.Interface.Events); n > 0 {
			_, _ = fmt.Fprintf(w, "  events     %d\n", n)
		}
	}
}
