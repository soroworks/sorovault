package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/store"
)

func newExportCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "export [file]",
		Short: "Write the registry as versioned JSON",
		Long: `Write every contract and every stored interface version, with their
timestamps, as one versioned JSON document. With no file, or with "-", it goes
to standard output.

The document is portable between databases: "sorovault import" loads it
without contacting RPC.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && output != "" {
				return fmt.Errorf("give either a file argument or --output, not both")
			}
			path := output
			if len(args) == 1 {
				path = args[0]
			}

			return withStore(cmd.Context(), func(ctx context.Context, s store.Store) error {
				snapshot, err := s.Export(ctx)
				if err != nil {
					return err
				}

				if path == "" || path == "-" {
					return writeJSON(cmd.OutOrStdout(), snapshot)
				}

				file, err := os.Create(path) //nolint:gosec // the path is the operator's --output argument
				if err != nil {
					return fmt.Errorf("creating %s: %w", path, err)
				}
				if err := writeJSON(file, snapshot); err != nil {
					_ = file.Close()
					return fmt.Errorf("writing %s: %w", path, err)
				}
				if err := file.Close(); err != nil {
					return fmt.Errorf("writing %s: %w", path, err)
				}
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s (%d contract(s))\n", path, len(snapshot.Contracts))
				return nil
			})
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this file instead of stdout")
	return cmd
}
