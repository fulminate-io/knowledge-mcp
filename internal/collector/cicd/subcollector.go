// SPDX-License-Identifier: Apache-2.0

package cicd

import "context"

// SubCollector collects resources of a specific type from a CI/CD provider.
// Each subcollector fully encapsulates its SDK types -- only ResourceSpec,
// EdgeSpec, and CollectTarget escape. SDK types never leak past this boundary.
//
// Authentication and configuration are injected at construction time, not
// passed per-call. The context carries only cancellation and deadline signals
// (plus an optional CascadeSet for cross-provider deduplication).
type SubCollector interface {
	// Name returns a unique identifier for this subcollector
	// (e.g. "github-workflows", "gitlab-pipelines").
	Name() string

	// Collect discovers CI/CD resources and returns them as specs.
	// The returned SubCollectorResult contains resources, edges to other
	// resources, and optional cascade targets for cross-provider discovery.
	Collect(ctx context.Context) (SubCollectorResult, error)
}
