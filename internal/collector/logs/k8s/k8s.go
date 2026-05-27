// SPDX-License-Identifier: Apache-2.0

// Package k8s implements a logwire.Provider backed by Kubernetes Events
// (events.k8s.io/v1). Each cluster Event is surfaced as a log entry so the
// existing logs pipeline can cluster, filter, and correlate Events the same
// way it handles CloudWatch, Loki, and Stackdriver output.
//
// The provider self-registers via init() so importing this package (even as
// a blank import) is enough to make "k8s" available through logwire.New("k8s").
// Authentication is delegated to client-go's standard kubeconfig loader —
// callers supply the kubecontext name via Configure and the provider
// resolves credentials from the operator's environment (KUBECONFIG,
// ~/.kube/config, or in-cluster service account). No credential material
// ever transits the knowledge graph, mirroring the design choice made for
// cloud/k8s.
package k8s

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/client-go/kubernetes"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

func init() {
	logwire.Register("k8s", func() logwire.Provider { return &k8sProvider{} })
}

// Compile-time assertion that k8sProvider implements logwire.Provider. Catches
// drift in the interface during refactors at build time rather than at the
// first runtime New("k8s") call.
var _ logwire.Provider = (*k8sProvider)(nil)

// k8sProvider implements logwire.Provider for Kubernetes Events. One provider
// instance serves a single kubecontext; multi-cluster queries register one
// log_backend per context.
//
// The clientset is built lazily on the first Collect/ListSources call so
// that Configure is cheap and never blocks on API discovery — this matches
// the stackdriver provider's ensureClient pattern and keeps configuration
// errors (missing kubeconfig, stale context) deferred until the operator
// actually asks for logwire.
type k8sProvider struct {
	// kubeContext is the kubeconfig context name to target. When empty the
	// provider falls back to the kubeconfig's current-context and then to
	// in-cluster discovery.
	kubeContext string

	// mu guards the lazy clientset so concurrent Collect/ListSources calls
	// on the same provider instance don't race to build the REST client.
	mu        sync.Mutex
	clientset kubernetes.Interface
}

// Configure applies provider-specific settings from the config map.
// Supported keys:
//   - kube_context (primary): kubeconfig context name to target. Also
//     accepted via the universal "url" slot so operators can configure the
//     provider through manage(configure_log_backend) without extending the
//     log_backend schema — for K8s the "backend URL" is semantically the
//     context identifier.
//   - credential (optional): reserved for future auth modes (e.g. inline
//     kubeconfig content). Currently ignored; auth is resolved from the
//     operator's environment.
//
// An empty kube_context is permitted — the provider falls through to the
// kubeconfig's current-context and then to in-cluster config, matching
// `kubectl`'s default behavior.
func (p *k8sProvider) Configure(cfg map[string]string) error {
	kubeContext := cfg["kube_context"]
	if kubeContext == "" {
		kubeContext = cfg["url"]
	}

	p.mu.Lock()
	p.kubeContext = kubeContext
	// Reset the lazy clientset so the next Collect/ListSources rebuilds it
	// under the new context. Mirrors stackdriver's Configure behavior.
	p.clientset = nil
	p.mu.Unlock()
	return nil
}

// ensureClientset lazily builds and caches the k8s clientset for the
// configured kubecontext. Thread-safe via p.mu.
//
// Returned errors wrap the underlying client-go failure (missing kubeconfig,
// unreachable API server, invalid context) so callers can surface actionable
// diagnostics to the operator.
func (p *k8sProvider) ensureClientset() (kubernetes.Interface, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clientset != nil {
		return p.clientset, nil
	}

	cfg, err := buildRestConfig(p.kubeContext)
	if err != nil {
		return nil, fmt.Errorf("k8s logs: build rest config: %w", err)
	}
	cs, err := newClientset(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s logs: build clientset: %w", err)
	}
	p.clientset = cs
	return p.clientset, nil
}

// contextForLogging returns a human-readable context identifier for use in
// error messages and log output. Empty contexts render as "<default>" so
// operators can distinguish "not configured" from the literal string "".
func (p *k8sProvider) contextForLogging(ctx context.Context) string {
	_ = ctx // reserved for future per-request context-name overrides
	if p.kubeContext == "" {
		return "<default>"
	}
	return p.kubeContext
}
