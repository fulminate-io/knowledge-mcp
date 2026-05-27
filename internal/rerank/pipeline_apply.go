// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// rerank_pipeline_apply.go owns the Pipeline-level fan-out: ApplyPre and
// ApplyPost. Split from rerank_pipeline.go solely to keep both files under
// the 500-line cap; no new exports beyond the two methods themselves. The
// per-op Apply implementations (FilterOp/ScoreOp/LimitOp) stay alongside
// their type definitions in rerank_pipeline.go.

// ApplyPre runs every op in p.Pre against `in` in order, threading the
// previous op's output into the next op's input. It returns the final slice
// (the output of the last op) and a hard error in the filter-all-of-N case.
//
// Pass-through cases (return in, nil with no error):
//   - p == nil (nil receiver — caller may skip storing a pipeline at all).
//   - len(p.Pre) == 0 (legal: a pipeline may run only post ops).
//   - len(in) == 0 (upstream "no match" is not a pipeline configuration error).
//
// Hard-error case: error fires ONLY when all three of the following hold —
// p.Pre is non-empty, in is non-empty, and the final output is empty. This
// is the "filter-all-of-N" sentinel: a pipeline that drops every candidate
// is almost always a configuration mistake worth surfacing rather than
// silently returning zero results to the caller.
//
// Per-op errors are wrapped with phase + op name and propagated up
// immediately — subsequent ops do not run.
func (p *Pipeline) ApplyPre(query string, in []engine.SearchResult) ([]engine.SearchResult, error) {
	if p == nil || len(p.Pre) == 0 || len(in) == 0 {
		return in, nil
	}
	current := in
	for _, op := range p.Pre {
		out, err := op.Apply(query, current)
		if err != nil {
			return nil, fmt.Errorf("pre op %s: %w", op.Name(), err)
		}
		current = out
	}
	if len(current) == 0 {
		return nil, fmt.Errorf("pipeline filtered all %d candidates → 0 in pre", len(in))
	}
	return current, nil
}

// ApplyPost runs every op in p.Post against `in` in order. Same shape and
// semantics as ApplyPre — see that doc comment for the pass-through and
// hard-error rules. The error message names "post" instead of "pre".
func (p *Pipeline) ApplyPost(query string, in []engine.SearchResult) ([]engine.SearchResult, error) {
	if p == nil || len(p.Post) == 0 || len(in) == 0 {
		return in, nil
	}
	current := in
	for _, op := range p.Post {
		out, err := op.Apply(query, current)
		if err != nil {
			return nil, fmt.Errorf("post op %s: %w", op.Name(), err)
		}
		current = out
	}
	if len(current) == 0 {
		return nil, fmt.Errorf("pipeline filtered all %d candidates → 0 in post", len(in))
	}
	return current, nil
}
