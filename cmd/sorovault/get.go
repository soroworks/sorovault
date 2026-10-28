package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/store"
)

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
	registerContractIDCompletion(cmd)
	registerNetworkCompletion(cmd)
	return cmd
}

// printInterface writes a contract's interface as readable signatures.
func printInterface(w io.Writer, s *store.Spec) {
	_, _ = fmt.Fprintf(w, "%s  (%s)\n", s.ContractID, s.Network)
	_, _ = fmt.Fprintf(w, "wasm %s\n\n", s.WasmHash)

	if len(s.Interface.Functions) == 0 {
		_, _ = fmt.Fprintln(w, "no functions")
	}
	for _, fn := range s.Interface.Functions {
		_, _ = fmt.Fprintf(w, "  %s\n", fn.Signature())
	}

	t := s.Interface.Types
	if n := len(t.Structs) + len(t.Unions) + len(t.Enums) + len(t.ErrorEnums); n > 0 {
		_, _ = fmt.Fprintln(w, "\ntypes:")
		for _, x := range t.Structs {
			_, _ = fmt.Fprintf(w, "  struct %s (%d fields)\n", x.Name, len(x.Fields))
		}
		for _, x := range t.Unions {
			_, _ = fmt.Fprintf(w, "  enum   %s (%d cases)\n", x.Name, len(x.Cases))
		}
		for _, x := range t.Enums {
			_, _ = fmt.Fprintf(w, "  enum   %s (%d cases, u32)\n", x.Name, len(x.Cases))
		}
		for _, x := range t.ErrorEnums {
			_, _ = fmt.Fprintf(w, "  error  %s (%d cases)\n", x.Name, len(x.Cases))
		}
	}

	if len(s.Interface.Events) > 0 {
		_, _ = fmt.Fprintln(w, "\nevents:")
		for _, e := range s.Interface.Events {
			_, _ = fmt.Fprintf(w, "  %s (%d params)\n", e.Name, len(e.Params))
		}
	}
}

// printFunction writes one function's signature in detail.
func printFunction(w io.Writer, fn model.Function) {
	_, _ = fmt.Fprintf(w, "%s\n", fn.Signature())

	if fn.Doc != "" {
		for _, line := range strings.Split(strings.TrimRight(fn.Doc, "\n"), "\n") {
			_, _ = fmt.Fprintf(w, "  // %s\n", line)
		}
	}

	if len(fn.Inputs) > 0 {
		_, _ = fmt.Fprintln(w, "\ninputs:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, in := range fn.Inputs {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\n", in.Name, in.Type.Display)
		}
		_ = tw.Flush()
	}

	_, _ = fmt.Fprint(w, "\nreturns: ")
	if len(fn.Outputs) == 0 {
		_, _ = fmt.Fprintln(w, "()")
		return
	}
	_, _ = fmt.Fprintln(w, fn.Outputs[0].Display)
}
