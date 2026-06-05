// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// compileMutateByIDLinkUnlink lowers a by-id link/unlink (from→to with a
// relationship) into a LINK/UNLINK MutationPlan: the source node rides as
// Selection.Ids=[from], the edge is EdgeSpec{relationship, to_id, forward:true}
// (the from node is the edge SOURCE). The cross-graph link_graph case was
// already default-denied upstream (compileMutate); an intra-practice/
// transformers direct link now Target-routes here (mutationRequest carries the
// graph). All three of from/to/relationship are required; a missing one falls
// through to legacy.
//
// LINK additionally carries edge metadata AS-GIVEN onto the EdgeSpec
// (weight/confidence/method/evidence/last_validated): these are client-supplied
// inputs the engine stores verbatim (generic-litmus). last_validated arrives as
// an RFC3339 string and rides the wire as int64 unix-nanos; a parse failure
// falls through to legacy (ok=false) so the RFC3339 error surfaces there. The
// UNLINK arm leaves the metadata fields zero (edge identity is from/to/type).
func compileMutateByIDLinkUnlink(a mutateArgs) (*knowledgev1.ExecuteRequest, bool) {
	if a.From == "" || a.To == "" || a.Relationship == "" {
		return nil, false
	}
	kind := knowledgev1.MutationPlan_MUTATION_KIND_LINK
	if a.Operation == "unlink" {
		kind = knowledgev1.MutationPlan_MUTATION_KIND_UNLINK
	}
	spec := &knowledgev1.EdgeSpec{
		// Client canonicalizes the per-graph relationship casing: the
		// engine stores the relationship AS-GIVEN, so the client
		// produces the canonical casing before it rides the wire.
		Relationship: canonicalEdgeCasing(a.Graph, a.Relationship),
		ToId:         a.To,
		Forward:      true,
	}
	// Edge metadata only rides on a LINK (UNLINK keys on from/to/type — the
	// metadata is ignored server-side, so we leave the EdgeSpec fields zero).
	if kind == knowledgev1.MutationPlan_MUTATION_KIND_LINK {
		lastNanos, ok := parseLastValidatedNanos(a.LastValidated)
		if !ok {
			return nil, false // unparseable RFC3339 → fall through to legacy, which surfaces the error.
		}
		spec.Weight = a.Weight
		spec.Confidence = a.Confidence
		spec.Method = a.Method
		spec.Evidence = a.EdgeEvidence
		spec.LastValidated = lastNanos
	}
	plan := &knowledgev1.MutationPlan{
		Kind:      kind,
		Selection: &knowledgev1.Selection{Ids: []string{a.From}},
		EdgeSpec:  spec,
	}
	return mutationRequest(plan, a), true
}

// parseLastValidatedNanos parses the EdgeSpec.last_validated wire form: an empty
// string is the unset case (0 nanos, ok=true), otherwise an RFC3339 timestamp is
// parsed and returned as UnixNano. time.Parse(time.RFC3339, ...) accepts
// fractional seconds, so a sub-second timestamp marshaled by LinkOneWithMeta
// (which Formats with RFC3339Nano) round-trips its full precision. A malformed
// (non-empty, non-RFC3339) value returns ok=false so the caller falls through to
// legacy, where the RFC3339 parse error surfaces to the LLM.
func parseLastValidatedNanos(s string) (int64, bool) {
	if s == "" {
		return 0, true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, false
	}
	return t.UnixNano(), true
}
