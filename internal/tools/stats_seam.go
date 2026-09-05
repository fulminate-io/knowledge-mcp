// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// stats_seam.go derives the engine.StatsFn that edge-type resolution reads a
// graph's edge vocabulary through, from the GraphCaller the tools layer holds.

// statsFnOf returns the Stats seam behind gc.
//
// GraphCaller is deliberately Execute-only (deps.go), so Stats is reached by
// the SAME narrow type assertion the per-graph stats arms already use — the
// statsRPC interface in intercept_query_cloud_cicd.go. Widening GraphCaller
// itself would hand every tools-layer consumer a capability only these paths
// need.
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
