// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pass_reads_recorder_test.go holds the per-pass read RECORDER and its accessors,
// split out of pass_reads_count_test.go when that file crossed the project's 500-line
// convention. The split is recorder-vs-gates: the classification machinery lives here,
// and every test that asserts on it stays in pass_reads_count_test.go — including
// TestPassReads_OneReadPerKindPerPass, whose region a landed criterion extracts from
// that file by name.

// corpusNodesSlice returns thoughts + charges + sessions — exactly the three types
// corpusNodeTypes covers (loop_corpus.go) — so cacheFromNodes yields a cache with the
// same TYPE COVERAGE the production delta drain gives. Seeding thoughts alone leaves
// ChargeSnapshot warm-but-empty (it reports cold only for a WHOLLY empty cache),
// which silently disables the tension path these counts are meant to observe AND
// pushes the charge and session hydrates onto their residual wire reads.
func (f *reflectEquivFake) corpusNodesSlice() []*knowledgev1.Node {
	out := f.thoughtNodesSlice()
	for _, c := range f.charges {
		out = append(out, cloneNode(c))
	}
	for _, s := range f.sessions {
		out = append(out, cloneNode(s))
	}
	return out
}

// countingWireFake classifies every request BEFORE delegating to the embedded
// reflectEquivFake, so each read the pass issues lands in exactly one counter. The
// counters are deliberately narrow: an unrecognized read (the EdgeEvidencedBy walk,
// the tension edge read, the topic-doc browse, the by-id watermark reads) is counted
// nowhere rather than smeared into a neighboring bucket.
//
// extra serves ids the fixture does not hold — the non-corpus stand-in the residual
// gate hydrates.
type countingWireFake struct {
	*reflectEquivFake
	extra map[string]*knowledgev1.Node
	// citedCode layers the thought->proxy->code-node chain onto the fixture when
	// non-nil, so a pass can be observed still RESOLVING cited code while issuing
	// zero cited-code edge reads of its own. The seed is declared once in
	// pass_reads_citedcode_test.go and consulted here, never restated.
	citedCode *citedCodeSeed

	mu                      sync.Mutex
	unifiedPivotEdgeReads   int
	adjacencyEdgeReads      int
	kgContainsEdgeReads     int
	chargedByThoughtReads   int
	chargedByChargeReads    int
	citedCodeRelatesToReads int
	// The two reads outside this collapse that a non-quiet pass still issues, named so
	// the default bucket below can be asserted at zero.
	evidencedByReads     int
	tensionUniverseReads int
	// otherEdgeReads counts every edge read whose type set matches none of the
	// buckets above — counted rather than smeared into a neighbor or dropped.
	otherEdgeReads     int
	corpusNodeHydrates int
	chargeTypeBrowses  int
	hydrateIDs         [][]string
}

func (f *countingWireFake) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if q := req.GetQuery(); q != nil {
		f.classify(q)
	}
	resp, err := f.reflectEquivFake.Execute(ctx, req)
	if err != nil {
		return resp, err
	}
	if f.citedCode != nil {
		if seeded, ok := f.citedCode.answer(req); ok {
			if resp == nil {
				resp = &knowledgev1.ExecuteResponse{}
			}
			resp.Edges = append(resp.Edges, seeded.GetEdges()...)
			resp.Nodes = append(resp.Nodes, seeded.GetNodes()...)
		}
	}
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return resp, nil
	}
	for _, id := range q.GetIds() {
		if n, ok := f.extra[id]; ok {
			resp.Nodes = append(resp.Nodes, n)
		}
	}
	return resp, nil
}

// classify buckets one query plan. Edge reads are keyed by the requested edge-type
// set; the EdgeChargedBy read is split by PIVOT — thought ids (the per-thought charge
// map) versus charge ids (the tension universe's own read, wire_tensions.go) — so the
// two are never mistaken for duplicates of each other.
func (f *countingWireFake) classify(q *knowledgev1.QueryPlan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := q.GetIds()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		switch types := q.GetSelection().GetEdgeTypes(); {
		case sameEdgeSet(types, unifiedPivotEdgeTypes):
			f.unifiedPivotEdgeReads++
		case sameEdgeSet(types, adjacencyEdgeTypes):
			f.adjacencyEdgeReads++
		case sameEdgeSet(types, []kgtypes.EdgeType{kgtypes.EdgeKGContains}):
			f.kgContainsEdgeReads++
		case sameEdgeSet(types, []kgtypes.EdgeType{kgtypes.EdgeChargedBy}):
			// SPLIT ON THE BAND, NOT ON THE PIVOT TYPE. These two readers were once
			// told apart by `allOfType(ids, NodeCharge)` — the tension universe pivoted
			// on charge ids, the per-thought charge map on thought ids. The tension
			// universe now issues a BANDED MATCH-ALL plan carrying NO ids at all, and
			// allOfType is false for an empty slice, so the old test silently routed it
			// into chargedByThoughtReads — the bucket asserted ZERO — and dropped
			// chargedByChargeReads to 0. That would have left chargedByCharge, which is
			// this test's KNOWN-POSITIVE CONTROL, permanently unfalsifiable while every
			// other leg stayed green.
			//
			// Presence of a from_id band is the property that survives the conversion:
			// the banded match-all read is the tension universe's, a pivot-bearing one
			// is the per-thought charge read. Both buckets keep meaning what their
			// names say.
			if q.GetEdgeFromBand() != nil {
				f.chargedByChargeReads++
			} else {
				f.chargedByThoughtReads++
			}
		case sameEdgeSet(types, []kgtypes.EdgeType{kgtypes.EdgeRelatesTo}):
			f.citedCodeRelatesToReads++
		// The two reads a non-quiet pass legitimately issues that no bucket above
		// covers. They are classified BY NAME rather than left to the default bucket so
		// that bucket can be asserted at ZERO — otherwise "unclassified" would mean
		// "these two plus anything new", and a novel read would hide among them.
		case sameEdgeSet(types, evidenceAdjEdgeTypes):
			f.evidencedByReads++
		case sameEdgeSet(types, tensionEdgeTypes):
			f.tensionUniverseReads++
		default:
			// DEFAULT BUCKET — the catch-all that makes the zeros above mean "no such
			// read happened" rather than "no such read is observable". Without it a read
			// over ANY unclassified type set lands in no counter, and every equality leg
			// stays green while the pass quietly issues it. Proven by injection: seven
			// novel-type-set reads left all five legs green before this arm existed.
			f.otherEdgeReads++
		}
		return
	}
	if len(ids) == 0 {
		if kgtypes.NodeType(q.GetSelection().GetNodeType()) == kgtypes.NodeCharge {
			f.chargeTypeBrowses++
		}
		return
	}
	f.hydrateIDs = append(f.hydrateIDs, append([]string(nil), ids...))
	if f.allCorpusTyped(ids) {
		f.corpusNodeHydrates++
	}
}

// allCorpusTyped reports whether every id names a thought, charge or session — the
// three types the resident cache covers.
func (f *countingWireFake) allCorpusTyped(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		switch f.typeOfID(id) {
		case kgtypes.NodeThought, kgtypes.NodeCharge, kgtypes.NodeThoughtSession:
		default:
			return false
		}
	}
	return true
}

func (f *countingWireFake) typeOfID(id string) kgtypes.NodeType {
	if n := f.reflectEquivFake.nodeByID(id); n != nil {
		return kgtypes.NodeType(n.GetType())
	}
	if n, ok := f.extra[id]; ok {
		return kgtypes.NodeType(n.GetType())
	}
	return ""
}

// sameEdgeSet compares a request's edge-type strings against an expected set,
// order-independently.
func sameEdgeSet(got []string, want []kgtypes.EdgeType) bool {
	if len(got) != len(want) {
		return false
	}
	wantSet := make(map[string]bool, len(want))
	for _, et := range want {
		wantSet[string(et)] = true
	}
	for _, g := range got {
		if !wantSet[g] {
			return false
		}
	}
	return true
}

func (f *countingWireFake) counts() (adjacency, kgContains, chargedByThought, chargedByCharge, hydrates, chargeBrowses int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adjacencyEdgeReads, f.kgContainsEdgeReads, f.chargedByThoughtReads,
		f.chargedByChargeReads, f.corpusNodeHydrates, f.chargeTypeBrowses
}

// relatesToReads is a NARROW accessor rather than a seventh return value on counts():
// counts() is destructured at three sites, two of them blank-heavy, so every value
// added there rewrites all three and invites a silently wrong arity.
func (f *countingWireFake) relatesToReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.citedCodeRelatesToReads
}

// unifiedReads is the second narrow accessor, on the same rule as relatesToReads:
// counts() stays at six values so its three destructuring sites are untouched.
func (f *countingWireFake) unifiedReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unifiedPivotEdgeReads
}

// otherReads exposes the default bucket, on the same narrow-accessor rule.
func (f *countingWireFake) otherReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.otherEdgeReads
}
