package cli

import (
	"github.com/spf13/cobra"

	"github.com/tylermac92/kscope/internal/k8s"
	"github.com/tylermac92/kscope/internal/render"
	"github.com/tylermac92/kscope/internal/resources"
)

func newResourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resources",
		Short: "Requests/limits vs. node allocatable capacity, and headroom",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := k8s.NewClientset(k8s.Config{Kubeconfig: kubeconfig, Context: kubeContext})
			if err != nil {
				return err
			}

			report, err := resources.Analyze(cmd.Context(), client, resources.Options{
				Namespace:     namespace,
				AllNamespaces: allNamespaces,
			})
			if err != nil {
				return err
			}

			return render.Render(cmd.OutOrStdout(), report, output)
		},
	}
}
