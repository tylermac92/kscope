package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Cluster-wide health rollup: node conditions, pod restarts, pending pods, PVCs",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
			return nil
		},
	}
}
