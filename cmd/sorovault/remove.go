package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

func newRemoveCmd() *cobra.Command {
	var (
		network string
		yes     bool
	)

	cmd := &cobra.Command{
		Use:   "remove <contract_id>",
		Short: "Delete a contract and all its stored versions",
		Long: `Delete a contract from the registry, together with every interface
version stored for it on the selected network.

The API deliberately has no route for this: it is unauthenticated, so
removal is an operator action available only through the CLI. Without --yes
the command asks for confirmation before deleting anything.`,
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

				if !yes {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Remove %s from %s and all its versions? [y/N] ", contractID, net)
					if !confirmed(cmd.InOrStdin()) {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "aborted")
						return nil
					}
				}

				if err := a.registry.Store().Delete(ctx, net, contractID); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed %s from %s\n", contractID, net)
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&network, "network", "", "network to remove from (default: the network RPC_URL serves)")
	cmd.Flags().BoolVar(&yes, "yes", false, "delete without asking for confirmation")
	return cmd
}

// confirmed reads one answer and accepts only an explicit yes.
func confirmed(r io.Reader) bool {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
