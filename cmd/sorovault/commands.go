package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/store"
)

func newAddCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "add <contract_id>...",
		Short: "Fetch, decode and register one or more contracts",
		Long: `Fetch each contract's WASM from the network, decode the interface
embedded in it, and store the result.

Adding a contract that is already registered is not an error: it re-checks
the contract against the network and records a new interface version if the
contract has been upgraded since it was last seen.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				var failed int

				for _, contractID := range args {
					fetchCtx, cancel := a.fetchContext(ctx)
					result, err := a.registry.Register(fetchCtx, contractID)
					cancel()

					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", contractID, err)
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
					return fmt.Errorf("%w: %d of %d contract(s) failed", errPrinted, failed, len(args))
				}
				return nil
			})
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the full result as JSON")
	return cmd
}

func newRefreshCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "refresh <contract_id>...",
		Short: "Re-check registered contracts for an upgraded interface",
		Long: `Compare each registered contract against the network and record a new
interface version if its WASM has changed.

Unlike "add", refresh starts from what is already stored, so an unchanged
contract costs a single RPC round trip: the module is only downloaded once
its hash is known to differ.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				var failed int

				for _, contractID := range args {
					fetchCtx, cancel := a.fetchContext(ctx)
					result, err := a.registry.Refresh(fetchCtx, contractID)
					cancel()

					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", contractID, err)
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
						fmt.Fprintf(cmd.OutOrStdout(), "%s upgraded to %s\n",
							result.Contract.ContractID, result.Contract.CurrentWasmHash)
						continue
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s unchanged\n", result.Contract.ContractID)
				}

				if failed > 0 {
					return fmt.Errorf("%w: %d of %d contract(s) failed", errPrinted, failed, len(args))
				}
				return nil
			})
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the full result as JSON")
	return cmd
}

func newListCmd() *cobra.Command {
	var (
		query   string
		network string
		limit   int
		offset  int
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered contracts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				filter := store.ListFilter{
					Query:   query,
					Network: network,
					Limit:   limit,
					Offset:  offset,
				}

				page, err := a.registry.Store().ListContracts(ctx, filter)
				if err != nil {
					return err
				}

				if asJSON {
					return writeJSON(cmd.OutOrStdout(), page)
				}

				if len(page.Contracts) == 0 {
					fmt.Fprintln(cmd.ErrOrStderr(), "no contracts registered")
					return nil
				}

				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "CONTRACT ID\tNETWORK\tWASM HASH\tLAST REFRESHED\tMATCHES")
				for _, c := range page.Contracts {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						c.ContractID, c.Network, truncate(c.CurrentWasmHash, 12),
						c.LastRefreshed.UTC().Format("2006-01-02 15:04"),
						strings.Join(c.Matches, ","))
				}
				if err := tw.Flush(); err != nil {
					return err
				}

				fmt.Fprintf(cmd.ErrOrStderr(), "\n%d of %d contract(s)\n", len(page.Contracts), page.Total)
				return nil
			})
		},
	}

	cmd.Flags().StringVarP(&query, "query", "q", "", "search contract ID, name, and function, type and event names")
	cmd.Flags().StringVar(&network, "network", "", "restrict to one network")
	cmd.Flags().IntVar(&limit, "limit", store.DefaultLimit, "maximum rows to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "rows to skip")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit results as JSON")
	return cmd
}

func newGetCmd() *cobra.Command {
	var (
		network  string
		wasmHash string
		function string
		asJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "get <contract_id>",
		Short: "Show a registered contract's decoded interface",
		Long: `Print a contract's decoded interface.

With --json the output is the same ABI document the HTTP API serves, so it
can be piped straight into other tooling.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				contractID := args[0]

				net := network
				if net == "" {
					var err error
					if net, err = a.registry.Network(ctx); err != nil {
						return err
					}
				}

				stored, err := a.registry.Store().GetSpec(ctx, net, contractID, wasmHash)
				if err != nil {
					return err
				}

				if function != "" {
					fn, ok := stored.Interface.Function(function)
					if !ok {
						return fmt.Errorf("no function %q in contract %s", function, contractID)
					}
					if asJSON {
						return writeJSON(cmd.OutOrStdout(), fn)
					}
					printFunction(cmd.OutOrStdout(), fn)
					return nil
				}

				if asJSON {
					return writeJSON(cmd.OutOrStdout(), stored.Interface)
				}
				printInterface(cmd.OutOrStdout(), stored)
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&network, "network", "", "network to read from (default: the network RPC_URL serves)")
	cmd.Flags().StringVar(&wasmHash, "wasm-hash", "", "show a specific version instead of the current one")
	cmd.Flags().StringVar(&function, "function", "", "show only this function")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the ABI as JSON")
	return cmd
}

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Args:  cobra.NoArgs,
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

			fmt.Fprintf(cmd.OutOrStdout(), "database is at migration version %d\n", version)
			return nil
		},
	}
	return cmd
}

// printRegisterResult writes the human-readable summary of an add.
func printRegisterResult(w io.Writer, r *registry.Result) {
	verb := "already current"
	switch {
	case r.Created:
		verb = "registered"
	case r.Changed:
		verb = "upgraded"
	}

	fmt.Fprintf(w, "%s %s on %s\n", r.Contract.ContractID, verb, r.Contract.Network)
	fmt.Fprintf(w, "  wasm hash  %s\n", r.Contract.CurrentWasmHash)

	if r.Interface != nil {
		fmt.Fprintf(w, "  functions  %d\n", len(r.Interface.Functions))
		if n := len(r.Interface.Types.Structs) + len(r.Interface.Types.Unions) +
			len(r.Interface.Types.Enums) + len(r.Interface.Types.ErrorEnums); n > 0 {
			fmt.Fprintf(w, "  types      %d\n", n)
		}
		if n := len(r.Interface.Events); n > 0 {
			fmt.Fprintf(w, "  events     %d\n", n)
		}
	}
}

// printInterface writes a contract's interface as readable signatures.
func printInterface(w io.Writer, s *store.Spec) {
	fmt.Fprintf(w, "%s  (%s)\n", s.ContractID, s.Network)
	fmt.Fprintf(w, "wasm %s\n\n", s.WasmHash)

	if len(s.Interface.Functions) == 0 {
		fmt.Fprintln(w, "no functions")
	}
	for _, fn := range s.Interface.Functions {
		fmt.Fprintf(w, "  %s\n", fn.Signature())
	}

	t := s.Interface.Types
	if n := len(t.Structs) + len(t.Unions) + len(t.Enums) + len(t.ErrorEnums); n > 0 {
		fmt.Fprintln(w, "\ntypes:")
		for _, x := range t.Structs {
			fmt.Fprintf(w, "  struct %s (%d fields)\n", x.Name, len(x.Fields))
		}
		for _, x := range t.Unions {
			fmt.Fprintf(w, "  enum   %s (%d cases)\n", x.Name, len(x.Cases))
		}
		for _, x := range t.Enums {
			fmt.Fprintf(w, "  enum   %s (%d cases, u32)\n", x.Name, len(x.Cases))
		}
		for _, x := range t.ErrorEnums {
			fmt.Fprintf(w, "  error  %s (%d cases)\n", x.Name, len(x.Cases))
		}
	}

	if len(s.Interface.Events) > 0 {
		fmt.Fprintln(w, "\nevents:")
		for _, e := range s.Interface.Events {
			fmt.Fprintf(w, "  %s (%d params)\n", e.Name, len(e.Params))
		}
	}
}

// printFunction writes one function's signature in detail.
func printFunction(w io.Writer, fn model.Function) {
	fmt.Fprintf(w, "%s\n", fn.Signature())

	if fn.Doc != "" {
		for _, line := range strings.Split(strings.TrimRight(fn.Doc, "\n"), "\n") {
			fmt.Fprintf(w, "  // %s\n", line)
		}
	}

	if len(fn.Inputs) > 0 {
		fmt.Fprintln(w, "\ninputs:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, in := range fn.Inputs {
			fmt.Fprintf(tw, "  %s\t%s\n", in.Name, in.Type.Display)
		}
		_ = tw.Flush()
	}

	fmt.Fprint(w, "\nreturns: ")
	if len(fn.Outputs) == 0 {
		fmt.Fprintln(w, "()")
		return
	}
	fmt.Fprintln(w, fn.Outputs[0].Display)
}

// writeJSON emits an indented JSON document followed by a newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
