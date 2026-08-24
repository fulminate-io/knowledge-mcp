// SPDX-License-Identifier: Apache-2.0

package contribhash

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// merge.go — collapse the emitted edge MULTISET onto the SET the server can
// actually store, before anything hashes it.

// edgeIdentity is the key two emitted rows must share to be the same stored row.
//
// THE FOUR STRING PARTS ARE THE SERVER'S IDENTITY, not a choice made here.
// lessEdgeKey (contribhash.go:428) orders on FromID, ToID, Type and Evidence
// before it ever looks at Weight, Confidence or Method, because those four are
// what the store keys a row on; the remaining fields are payload the store
// resolves last-wins.
//
// THE TWO INDICES ARE IN THE KEY, AND LEAVING THEM OUT WOULD BE DATA LOSS.
// WriteResult serves every graph family, not only code. A knowledge-graph
// collect names endpoints BY POSITION (FromIdx/ToIdx >= 0 with both ID fields
// empty), so on the four string parts alone every such edge of one type with no
// evidence would share a key and a whole graph's CONTAINS set would collapse to
// one row. The code collector emits only ID-addressed edges (parser.ToBatchEdges
// sets -1/-1 with both IDs), so for the family whose hashes are compared against
// a manifest both indices are the constant -1 and this key reduces to exactly
// the server's four parts.
type edgeIdentity struct {
	fromIdx  int
	toIdx    int
	fromID   string
	toID     string
	edgeType string
	evidence string
}

// identityOf projects an emitted edge onto the identity the store would file it
// under.
func identityOf(e kgwire.BatchEdge) edgeIdentity {
	return edgeIdentity{
		fromIdx:  e.FromIdx,
		toIdx:    e.ToIdx,
		fromID:   e.FromID,
		toID:     e.ToID,
		edgeType: string(e.Type),
		evidence: e.Evidence,
	}
}

// MergeEdgesByIdentity collapses copies of one stored identity into a single
// row: every field takes the LAST copy's value, except Weight, which is SUMMED
// across the copies. The survivor sits at the FIRST copy's position.
//
// IT IS CALLED MERGE RATHER THAN DEDUP BECAUSE IT AGGREGATES. A name saying only
// "dedup" would describe half of what happens and hide the summation from every
// future reader — the copies of a CALLS identity are not redundant, they each
// carry a share of one count.
//
// KEEP-LAST FOR THE UNSUMMED FIELDS IS AGREEMENT WITH THE STORE, not an
// inference from one statement. The server resolves within-batch rivals
// LAST-OP-WINS, uniformly: the cloud copy path takes the highest copy ordinal,
// the cloud batch path and the OSS input-order dedup were unified onto the same
// tie rule. A client that hashes a row set the server stores must pick the same
// winner, or its digest describes a row nobody holds.
//
// SUMMING IS CALL-COUNT SEMANTICS, and it needs no per-type allowlist. Weight is
// the CALL COUNT on a CALLS/TEST_CALLS edge (treesitter.Edge.Weight, types.go:247).
// weightedCallEdges (chunker_edges.go:224) aggregates call sites by callee
// SPELLING and does so BEFORE resolution, so two spellings in one declaration
// that bind to one target stay two rows, each holding its own share. Every other
// collector edge type carries Weight 0 by construction, so summing zeros is
// identical to keeping one copy and a future weighted type is correct here
// without anyone remembering to add a row.
//
// Confidence is deliberately NOT summed: it is a 1/N share of an ambiguity
// group, and group members carry distinct group keys in Evidence, so they are
// distinct identities that never meet in this merge at all.
//
// MEASURED CENSUS, TREE-DERIVED at commit c8afb0f8 over this repository and NOT
// a locked literal — re-derive it rather than quoting it. 219,448 emitted edges
// carrying total Weight 76,070 collapsed to 216,979 rows carrying total Weight
// 76,070: conserved exactly. 389 duplicated CALLS identities held 601 call
// counts that keeping a single copy would have published as never having
// happened; 366 duplicated IMPLEMENTS identities differed only in Method, which
// last-wins collapses the way the store already does.
//
// THE SURVIVOR'S POSITION IS THE FIRST COPY'S, which keeps the output order a
// stable function of the input order — the determinism the per-file hash depends
// on. Summing is order-independent, so it cannot disturb that.
func MergeEdgesByIdentity(edges []kgwire.BatchEdge) []kgwire.BatchEdge {
	if len(edges) == 0 {
		return edges
	}
	pos := make(map[edgeIdentity]int, len(edges))
	out := make([]kgwire.BatchEdge, 0, len(edges))
	for _, e := range edges {
		id := identityOf(e)
		if i, seen := pos[id]; seen {
			// Capture the running total, overwrite with the later copy, restore
			// the total. Written as a bare out[i] = e it would lose the sum
			// silently.
			summed := out[i].Weight + e.Weight
			out[i] = e
			out[i].Weight = summed
			continue
		}
		pos[id] = len(out)
		out = append(out, e)
	}
	return out
}
