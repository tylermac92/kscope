// Package cli defines kscope's Cobra command tree. Commands here stay thin:
// parse flags, call an analyzer in the corresponding internal/<command>
// package, and hand the result to internal/render. No business logic.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Global flag values, populated by Viper from flags/env/config file
// (in that precedence order) once the root command's PersistentPreRunE runs.
var (
	kubeconfig    string
	kubeContext   string
	namespace     string
	allNamespaces bool
	output        string
)

const envPrefix = "KSCOPE"

// NewRootCmd constructs the kscope root command and wires up all
// subcommands.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "kscope",
		Short: "A fast, opinionated health snapshot of a Kubernetes cluster",
		Long: `kscope gives an SRE a fast, opinionated health snapshot of a Kubernetes
cluster. It talks to the Kubernetes API directly via client-go rather than
wrapping kubectl.`,
		SilenceUsage:      true,
		PersistentPreRunE: initConfig,
	}

	pf := rootCmd.PersistentFlags()
	pf.StringVar(&kubeconfig, "kubeconfig", "", "path to the kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")
	pf.StringVar(&kubeContext, "context", "", "kubeconfig context to use (default: current-context)")
	pf.StringVarP(&namespace, "namespace", "n", "default", "namespace to operate in")
	pf.BoolVarP(&allNamespaces, "all-namespaces", "A", false, "operate across all namespaces")
	pf.StringVarP(&output, "output", "o", "table", "output format: table|json")

	bindViperFlag(pf, "kubeconfig")
	bindViperFlag(pf, "context")
	bindViperFlag(pf, "namespace")
	bindViperFlag(pf, "all-namespaces")
	bindViperFlag(pf, "output")

	rootCmd.AddCommand(
		newHealthCmd(),
		newEventsCmd(),
		newResourcesCmd(),
		newRBACCmd(),
		newDiffCmd(),
	)

	return rootCmd
}

// Execute runs the kscope root command.
func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func bindViperFlag(pf *pflag.FlagSet, name string) {
	if err := viper.BindPFlag(name, pf.Lookup(name)); err != nil {
		// Flags are registered immediately above, so a lookup miss here
		// is a programming error, not a runtime condition to recover from.
		panic(fmt.Sprintf("binding flag %q: %v", name, err))
	}
}

// initConfig wires up Viper's precedence order (env vars, then an optional
// ~/.kscope.yaml, then flags) and refreshes the package-level flag values
// from the merged result so subcommands see the final, resolved settings.
func initConfig(cmd *cobra.Command, _ []string) error {
	viper.SetEnvPrefix(envPrefix)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	home, err := os.UserHomeDir()
	if err == nil {
		viper.AddConfigPath(home)
		viper.SetConfigName(".kscope")
		viper.SetConfigType("yaml")
		if err := viper.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return fmt.Errorf("reading config file: %w", err)
			}
		}
	}

	kubeconfig = viper.GetString("kubeconfig")
	kubeContext = viper.GetString("context")
	namespace = viper.GetString("namespace")
	allNamespaces = viper.GetBool("all-namespaces")
	output = viper.GetString("output")

	return nil
}
