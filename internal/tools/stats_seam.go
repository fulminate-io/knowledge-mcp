// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// stats_seam.go holds the narrow Stats view of the graph client and the two
// adapters over it: the engine.StatsFn that edge-type resolution reads a graph's
// edge vocabulary through, and the engine.ExecuteFn the sample-fetch helpers
// consume. statsRPC MOVED HERE with the deletion of the per-account resource
// reader it used to be declared in; it was never that reader's own type, which
// is why it survived it.

// statsFnOf returns the Stats seam behind gc.
//
// GraphCaller is deliberately Execute-only (deps.go), so Stats is reached by
// the SAME narrow type assertion the graph-stats arms already use — the statsRPC
// interface below. Widening GraphCaller itself would hand every tools-layer
// consumer a capability only these paths need.
//
// A caller WITHOUT Stats is an ERROR, never a nil seam. A nil seam would let a
// call whose edge types cannot be resolved run anyway against whatever the
// caller happened to spell — which is precisely the silently-lost-resolution
// failure this change exists to remove. The error names the concrete type so
// the missing method is diagnosable rather than mysterious.
func statsFnOf(gc GraphCaller) (engine.StatsFn, error) {
	sr, ok := gc.(statsRPC)
	if !ok {
		return nil, fmt.Errorf(
			"graph client %T serves no Stats RPC, so edge types cannot be resolved against the graph's vocabulary", gc)
	}
	return sr.Stats, nil
}

// statsRPC is the narrow view of *GraphClient the stats/list paths need —
// Stats + Execute. Declared so the helpers can be tested with a fake.
type statsRPC interface {
	Stats(ctx context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error)
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// statsExecOf adapts a statsRPC to the engine.ExecuteFn the sample-fetch helper
// consumes.
func statsExecOf(gc statsRPC) engine.ExecuteFn {
	return gc.Execute
}
