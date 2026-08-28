package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Structured diff of deployments, configmaps, and resource configs between two contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
			return nil
		},
	}
}
