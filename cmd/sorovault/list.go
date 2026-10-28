package main

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/store"
)

func newListCmd() *cobra.Command {
	var (
		query     string
		network   string
		functions []string
		types     []string
		limit     int
		offset    int
		asJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered contracts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				filter := store.ListFilter{
					Query:     query,
					Network:   network,
					Functions: functions,
					Types:     types,
					Limit:     limit,
					Offset:    offset,
				}

				page, err := a.registry.Store().ListContracts(ctx, filter)
				if err != nil {
					return err
				}

				if asJSON {
					return writeJSON(cmd.OutOrStdout(), page)
				}

				if len(page.Contracts) == 0 {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "no contracts registered")
					return nil
				}

				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				_, _ = fmt.Fprintln(tw, "CONTRACT ID\tNETWORK\tWASM HASH\tLAST REFRESHED\tMATCHES\tSTATUS")
				for _, c := range page.Contracts {
					status := ""
					if c.MissingSince != nil {
						status = "missing"
					}
					if c.Verified() {
						status = strings.TrimSpace(status + " verified")
					}
					_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						c.ContractID, c.Network, truncate(c.CurrentWasmHash, 12),
						c.LastRefreshed.UTC().Format("2006-01-02 15:04"),
						strings.Join(c.Matches, ","), status)
				}
				if err := tw.Flush(); err != nil {
					return err
				}

				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\n%d of %d contract(s)\n", len(page.Contracts), page.Total)
				return nil
			})
		},
	}

	cmd.Flags().StringVarP(&query, "query", "q", "", "search contract ID, name, and function, type and event names")
	cmd.Flags().StringVar(&network, "network", "", "restrict to one network")
	cmd.Flags().StringArrayVar(&functions, "function", nil, "only contracts with a function of exactly this name (repeatable)")
	cmd.Flags().StringArrayVar(&types, "type", nil, "only contracts with a user-defined type of exactly this name (repeatable)")
	cmd.Flags().IntVar(&limit, "limit", store.DefaultLimit, "maximum rows to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "rows to skip")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit results as JSON")
	return cmd
}
