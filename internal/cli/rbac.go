package cli

import (
	"github.com/spf13/cobra"

	"github.com/tylermac92/kscope/internal/k8s"
	"github.com/tylermac92/kscope/internal/rbac"
	"github.com/tylermac92/kscope/internal/render"
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
	var includeSystem bool

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Flag service accounts and bindings with cluster-admin or wildcard verbs/resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := k8s.NewClientset(k8s.Config{Kubeconfig: kubeconfig, Context: kubeContext})
			if err != nil {
				return err
			}

			report, err := rbac.Analyze(cmd.Context(), client, rbac.Options{
				Namespace:     namespace,
				AllNamespaces: allNamespaces,
				IncludeSystem: includeSystem,
			})
			if err != nil {
				return err
			}

			return render.Render(cmd.OutOrStdout(), report, output)
		},
	}

	cmd.Flags().BoolVar(&includeSystem, "include-system", false, "include system:-prefixed built-in roles/subjects and system-namespace service accounts")

	return cmd
}
