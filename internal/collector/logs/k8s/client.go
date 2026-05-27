// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// buildRestConfig constructs a client-go *rest.Config for the named
// kubecontext. Resolution order matches cloud/k8s/client.go (intentionally
// duplicated rather than imported — logs/ sits below cloud/ in the package
// dependency pyramid, so this small helper lives alongside its consumer):
//
//  1. Kubeconfig via clientcmd's default loading rules ($KUBECONFIG or
//     $HOME/.kube/config). When contextName is non-empty it overrides the
//     current-context; when empty the kubeconfig's current-context wins.
//  2. In-cluster config (service account mounted at
//     /var/run/secrets/kubernetes.io/serviceaccount) when no kubeconfig is
//     available.
//
// Returns a descriptive error combining both failure modes when neither
// loader succeeds so operators can tell at a glance whether they need to
// set KUBECONFIG or run inside a cluster.
func buildRestConfig(contextName string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	cfg, kubeErr := kubeConfig.ClientConfig()
	if kubeErr == nil {
		return cfg, nil
	}

	cfg, inClusterErr := rest.InClusterConfig()
	if inClusterErr != nil {
		return nil, fmt.Errorf(
			"k8s logs: no kubeconfig (%v) and not in-cluster (%v)",
			kubeErr, inClusterErr,
		)
	}
	return cfg, nil
}

// newClientset wraps kubernetes.NewForConfig so callers get a uniformly
// wrapped error and a single choke point for future instrumentation
// (metrics, retry wrappers, etc.). Kept separate from buildRestConfig so
// tests can stub one layer without stubbing the other.
func newClientset(cfg *rest.Config) (kubernetes.Interface, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s logs: kubernetes.NewForConfig: %w", err)
	}
	return cs, nil
}
