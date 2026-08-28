package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRBACCmd() *cobra.Command {
	rbacCmd := &cobra.Command{
		Use:   "rbac",
		Short: "RBAC inspection commands",
	}

	rbacCmd.AddCommand(newRBACAuditCmd())

	return rbacCmd
}

func newRBACAuditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "audit",
		Short: "Flag service accounts and bindings with cluster-admin or wildcard verbs/resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "not implemented")
			return nil
		},
	}
}
