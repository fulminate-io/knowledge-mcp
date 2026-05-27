// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// clientBundle holds all k8s API clients needed by the collector.
type clientBundle struct {
	clientset       kubernetes.Interface
	dynamicClient   dynamic.Interface
	apiextensionsCS apiextensionsclient.Interface
	contextName     string
}

// apiextensionsCRDLister is the production crdLister using the apiextensions clientset.
type apiextensionsCRDLister struct {
	client apiextensionsclient.Interface
}

func (l *apiextensionsCRDLister) ListCRDs(ctx context.Context) ([]apiextensionsv1.CustomResourceDefinition, error) {
	list, err := l.client.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// buildClient creates a k8s client bundle from the given context name.
// If contextName is non-empty, it overrides the current-context in kubeconfig
// (used when cascading from EKS/GKE/AKS). Otherwise, it uses KUBECONFIG env
// with default loading rules. Falls back to in-cluster config if kubeconfig
// is not available.
func buildClient(contextName string) (*clientBundle, error) {
	cfg, resolvedContext, err := buildRestConfig(contextName)
	if err != nil {
		return nil, err
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: create clientset: %w", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: create dynamic client: %w", err)
	}

	extCS, err := apiextensionsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: create apiextensions client: %w", err)
	}

	return &clientBundle{
		clientset:       cs,
		dynamicClient:   dyn,
		apiextensionsCS: extCS,
		contextName:     resolvedContext,
	}, nil
}

// buildRestConfig creates a rest.Config and resolves the effective context name.
func buildRestConfig(contextName string) (*rest.Config, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}

	if contextName != "" {
		overrides.CurrentContext = contextName
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	// Try kubeconfig first.
	cfg, err := kubeConfig.ClientConfig()
	if err == nil {
		rawCfg, rawErr := kubeConfig.RawConfig()
		if rawErr != nil {
			return nil, "", fmt.Errorf("k8s: raw config: %w", rawErr)
		}
		resolved := rawCfg.CurrentContext
		if contextName != "" {
			resolved = contextName
		}
		return cfg, resolved, nil
	}

	// Fall back to in-cluster config.
	cfg, inClusterErr := rest.InClusterConfig()
	if inClusterErr != nil {
		return nil, "", fmt.Errorf("k8s: no kubeconfig (%v) and not in-cluster (%v)", err, inClusterErr)
	}
	return cfg, "in-cluster", nil
}
