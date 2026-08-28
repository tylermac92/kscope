package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newResourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resources",
		Short: "Requests/limits vs. node allocatable capacity, and headroom",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
			return nil
		},
	}
}
