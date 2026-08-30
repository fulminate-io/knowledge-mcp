// SPDX-License-Identifier: Apache-2.0

// Package linker is the client-side cross-graph linker. It walks the
// indexed graphs via gc.Call (read), discovers Tier-1 cross-graph
// relationships (container images → code repos, Helm charts → cloud
// workloads, Dockerfile COPY → source files, workload identity →
// service-account IAM), and emits derived edges into the linkage graph
// through emitLink, whose crossgraph.ResolveAndLink materializes the
// proxies client-side and writes the linkage-graph edge with metadata.
//
// Relocated from pkg/linker/ during the client/server separation. The package operates
// only through GraphCaller — it holds no in-process store engine —
// because the linker is a client-side process that drives the server
// over the wire.
package linker

import (
	"context"
	"errors"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// GraphCaller is the narrow interface the client linker needs to read +
// write through the knowledge MCP wire. Mirrors tools.GraphCaller without
// creating an import-cycle dependency on cmd/knowledge/internal/tools. The linker
// reads/emits over the Execute carrier (the helpers type-assert this to
// linkerExecutor).
type GraphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// LinkOptions controls linker behavior.
type LinkOptions struct {
	// DryRun, when true, runs discovery without emitting mutate(link)
	// edges. Useful for development / inspection. Currently every
	// sub-linker honors this by short-circuiting before its mutate(link)
	// call.
	DryRun bool
}

// LinkResult aggregates counts from all sub-linkers.
type LinkResult struct {
	ImageLinks            int
	HelmLinks             int
	DockerfileLinks       int
	WorkloadIdentityLinks int
	Errors                []error
}

// RunAll executes every sub-linker in sequence and returns aggregated
// counts. Best-effort: per-sub-linker failures collect into Errors but
// do not abort the remaining sub-linkers. Mirrors pkg/linker.RunAll's
// surface so the manage(link) intercept can swap in this implementation
// without churning callers.
func RunAll(ctx context.Context, gc GraphCaller, opts LinkOptions) (*LinkResult, error) {
	if gc == nil {
		return nil, errors.New("linker.RunAll: GraphCaller is required")
	}
	result := &LinkResult{}

	imageLinks, err := LinkImageTargets(ctx, gc, opts)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("image linker: %w", err))
	}
	result.ImageLinks = imageLinks

	helmLinks, err := LinkHelmCharts(ctx, gc, opts)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("helm linker: %w", err))
	}
	result.HelmLinks = helmLinks

	dockerfileLinks, err := LinkDockerfiles(ctx, gc, opts)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("dockerfile linker: %w", err))
	}
	result.DockerfileLinks = dockerfileLinks

	wiLinks, err := LinkWorkloadIdentity(ctx, gc, opts)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("workload identity linker: %w", err))
	}
	result.WorkloadIdentityLinks = wiLinks

	return result, nil
}
