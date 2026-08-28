package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tylermac92/kscope/internal/diff"
	"github.com/tylermac92/kscope/internal/k8s"
	"github.com/tylermac92/kscope/internal/render"
)

func newDiffCmd() *cobra.Command {
	var contextA, contextB string
	var includeSystem bool

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Structured diff of deployments, configmaps, and resource configs between two contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			clientA, err := k8s.NewClientset(k8s.Config{Kubeconfig: kubeconfig, Context: contextA})
			if err != nil {
				return fmt.Errorf("building clientset for context %q: %w", contextA, err)
			}
			clientB, err := k8s.NewClientset(k8s.Config{Kubeconfig: kubeconfig, Context: contextB})
			if err != nil {
				return fmt.Errorf("building clientset for context %q: %w", contextB, err)
			}

			report, err := diff.Analyze(cmd.Context(), clientA, clientB, diff.Options{
				Namespace:     namespace,
				AllNamespaces: allNamespaces,
				IncludeSystem: includeSystem,
				ContextA:      contextA,
				ContextB:      contextB,
			})
			if err != nil {
				return err
			}

			return render.Render(cmd.OutOrStdout(), report, output)
		},
	}

	cmd.Flags().StringVar(&contextA, "context-a", "", "first kubeconfig context to compare (required)")
	cmd.Flags().StringVar(&contextB, "context-b", "", "second kubeconfig context to compare (required)")
	cmd.Flags().BoolVar(&includeSystem, "include-system", false, "include kube-system/kube-public/kube-node-lease when comparing all namespaces")

	for _, flagName := range []string{"context-a", "context-b"} {
		if err := cmd.MarkFlagRequired(flagName); err != nil {
			// Flags are registered immediately above, so a lookup miss here
			// is a programming error, not a runtime condition to recover from.
			panic(fmt.Sprintf("marking flag %q required: %v", flagName, err))
		}
	}

	return cmd
}
