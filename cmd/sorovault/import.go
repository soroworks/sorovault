package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/store"
)

func newImportCmd() *cobra.Command {
	var (
		input string
		merge bool
	)

	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Load a registry exported as JSON",
		Long: `Load a document written by "sorovault export" into the registry.

Without --merge the registry must be empty; the import then reproduces every
contract, interface version and timestamp exactly. With --merge only the
contracts and versions that are missing are added, and records already in the
database are left alone.

Import reads the document directly into the store. It never contacts RPC, so
it works when the network is unavailable.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && input != "" {
				return fmt.Errorf("give either a file argument or --input, not both")
			}
			path := input
			if len(args) == 1 {
				path = args[0]
			}

			var data []byte
			var err error
			if path == "" || path == "-" {
				data, err = io.ReadAll(cmd.InOrStdin())
			} else {
				data, err = os.ReadFile(path) //nolint:gosec // the path is the operator's --input argument
			}
			if err != nil {
				return fmt.Errorf("reading snapshot: %w", err)
			}

			var snapshot store.Snapshot
			if err := json.Unmarshal(data, &snapshot); err != nil {
				return fmt.Errorf("reading snapshot: %w", err)
			}

			return withStore(cmd.Context(), func(ctx context.Context, s store.Store) error {
				if err := s.Import(ctx, &snapshot, merge); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "imported %d contract(s)\n", len(snapshot.Contracts))
				return nil
			})
		},
	}

	cmd.Flags().StringVarP(&input, "input", "i", "", "read from this file instead of stdin")
	cmd.Flags().BoolVar(&merge, "merge", false, "add only what is missing instead of requiring an empty registry")
	return cmd
}
