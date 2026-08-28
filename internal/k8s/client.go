// Package k8s constructs Kubernetes API clients from kubeconfig/context
// settings shared by every kscope command.
package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Config holds the connection settings needed to build a clientset.
type Config struct {
	// Kubeconfig is the path to a kubeconfig file. If empty, client-go's
	// standard loading rules (KUBECONFIG env var, then ~/.kube/config)
	// are used.
	Kubeconfig string
	// Context is the kubeconfig context to use. If empty, the
	// kubeconfig's current-context is used.
	Context string
}

// NewClientset builds a Kubernetes clientset for the given Config.
func NewClientset(cfg Config) (kubernetes.Interface, error) {
	restConfig, err := RESTConfig(cfg)
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes clientset: %w", err)
	}

	return clientset, nil
}

// RESTConfig resolves a *rest.Config for the given Config using client-go's
// standard kubeconfig loading rules, honoring an explicit kubeconfig path
// and/or context override when provided.
func RESTConfig(cfg Config) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if cfg.Kubeconfig != "" {
		loadingRules.ExplicitPath = cfg.Kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{}
	if cfg.Context != "" {
		overrides.CurrentContext = cfg.Context
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	return restConfig, nil
}
