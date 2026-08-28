package cli

import (
	"github.com/spf13/cobra"

	"github.com/tylermac92/kscope/internal/events"
	"github.com/tylermac92/kscope/internal/k8s"
	"github.com/tylermac92/kscope/internal/render"
)

func newEventsCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Deduplicated, grouped Warning events with a timeline",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := k8s.NewClientset(k8s.Config{Kubeconfig: kubeconfig, Context: kubeContext})
			if err != nil {
				return err
			}

			report, err := events.Analyze(cmd.Context(), client, events.Options{
				Namespace:     namespace,
				AllNamespaces: allNamespaces,
				All:           all,
			})
			if err != nil {
				return err
			}

			return render.Render(cmd.OutOrStdout(), report, output)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "show all event groups (disable the default cap)")

	return cmd
}
