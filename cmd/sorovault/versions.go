package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/store"
)

func newVersionsCmd() *cobra.Command {
	var (
		network string
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "versions <contract_id>",
		Short: "List a contract's stored interface versions",
		Long: `Print every interface SoroVault has stored for a contract, newest first.

The current version is the one the contract runs now; an older one can be
read with "get --wasm-hash".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				net := network
				if net == "" {
					var err error
					if net, err = a.registry.Network(ctx); err != nil {
						return err
					}
				}

				versions, err := a.registry.Store().ListVersions(ctx, net, args[0])
				if err != nil {
					return err
				}

				if asJSON {
					return writeJSON(cmd.OutOrStdout(), versionsDocument{
						ContractID: args[0],
						Network:    net,
						Versions:   versions,
					})
				}
				printVersions(cmd.OutOrStdout(), versions)
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&network, "network", "", "network to read from (default: the network RPC_URL serves)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the versions as JSON")
	registerContractIDCompletion(cmd)
	registerNetworkCompletion(cmd)
	return cmd
}

// versionsDocument is the JSON shape of the versions command. It matches
// what GET /api/contracts/{id}/versions serves, so the two are
// interchangeable in a pipeline.
type versionsDocument struct {
	ContractID string              `json:"contract_id"`
	Network    string              `json:"network"`
	Versions   []store.SpecVersion `json:"versions"`
}

// printVersions writes one row per stored interface, newest first.
func printVersions(w io.Writer, versions []store.SpecVersion) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "WASM HASH\tDECODED\tCURRENT")
	for _, v := range versions {
		current := ""
		if v.Current {
			current = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n",
			v.WasmHash, v.DecodedAt.UTC().Format("2006-01-02 15:04"), current)
	}
	_ = tw.Flush()
}
