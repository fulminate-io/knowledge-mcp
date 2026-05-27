// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"k8s.io/client-go/kubernetes"
)

// setClientset installs a pre-built clientset (typically a fake from
// k8s.io/client-go/kubernetes/fake) onto the provider, bypassing kubeconfig
// loading. Test-only helper — lives in a _test.go file so it never compiles
// into the production binary. Used by collect_test.go,
// collect_filters_test.go, sources_test.go, k8s_test.go, and
// integration_test.go to inject fakes for the real production filter / watch
// / source-listing logic.
func (p *k8sProvider) setClientset(cs kubernetes.Interface) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clientset = cs
}
