// SPDX-License-Identifier: Apache-2.0

// Package linker is the client-side cross-graph linker. It walks the
// indexed graphs via gc.Call (read), discovers the Dockerfile COPY → source
// file relationship, and emits derived edges into the linkage graph through
// emitLink, whose crossgraph.ResolveAndLink materializes the proxies
// client-side and writes the linkage-graph edge with metadata.
//
// IT USED TO CARRY FOUR PASSES AND NOW CARRIES ONE. The image, Helm-chart and
// workload-identity passes each read a CLOUD graph on one side of the
// relationship they discovered — container image → cloud workload, chart →
// cloud workload, k8s service account → cloud IAM identity — and the built-in
// cloud collectors that produced those graphs are gone. A pass whose one side
// has no producer discovers nothing, so they were deleted rather than left
// registered and answering empty. The Dockerfile pass reads code graphs on both
// sides and is untouched.
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

// LinkResult aggregates counts from the sub-linkers.
//
// THE THREE RETIRED COUNTERS ARE NOT KEPT AT ZERO. A field that is always zero
// reads to a caller as "the pass ran and found nothing", which is a different
// statement from "there is no such pass", and manage(link) renders these counts
// to an operator.
type LinkResult struct {
	DockerfileLinks int
	Errors          []error
}

// RunAll executes every sub-linker in sequence and returns aggregated
// counts. Best-effort: per-sub-linker failures collect into Errors but
// do not abort the remaining sub-linkers. The loop shape is kept for the one
// remaining pass because the aggregation contract — a failing pass records an
// error and does not abort the others — is what the caller relies on.
func RunAll(ctx context.Context, gc GraphCaller, opts LinkOptions) (*LinkResult, error) {
	if gc == nil {
		return nil, errors.New("linker.RunAll: GraphCaller is required")
	}
	result := &LinkResult{}

	dockerfileLinks, err := LinkDockerfiles(ctx, gc, opts)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("dockerfile linker: %w", err))
	}
	result.DockerfileLinks = dockerfileLinks

	return result, nil
}
