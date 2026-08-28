package cli

import (
	"github.com/spf13/cobra"

	"github.com/tylermac92/kscope/internal/health"
	"github.com/tylermac92/kscope/internal/k8s"
	"github.com/tylermac92/kscope/internal/render"
)

func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Cluster-wide health rollup: node conditions, pod restarts, pending pods, PVCs",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := k8s.NewClientset(k8s.Config{Kubeconfig: kubeconfig, Context: kubeContext})
			if err != nil {
				return err
			}

			report, err := health.Analyze(cmd.Context(), client, health.Options{
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
