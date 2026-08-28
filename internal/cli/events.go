package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events",
		Short: "Deduplicated, grouped Warning events with a timeline",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
			return nil
		},
	}
}
