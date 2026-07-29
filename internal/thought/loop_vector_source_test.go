// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// residentFake serves stored vectors from an in-memory map, the way the client's
// resident segment engines do. An id it does not hold returns (nil, false, nil) —
// the VECTORLESS case, which is NOT an error — unless failOn names it, which
// simulates an engine that could not be read at all.
type residentFake struct {
	vectors map[string][]byte
	calls   int
	failOn  string
}

func (r *residentFake) VectorByID(_ context.Context, externalID string) ([]byte, bool, error) {
	r.calls++
	if r.failOn != "" && externalID == r.failOn {
		return nil, false, errors.New("resident engine unavailable")
	}
	v, ok := r.vectors[externalID]
	return v, ok, nil
}

// gateFakeVerdict is a scripted coverage gate.
type gateFakeVerdict struct {
	trustworthy bool
	reason      string
	err         error
	calls       int
}

func (g *gateFakeVerdict) HNSWCoverageTrustworthy(_ context.Context) (bool, string, error) {
	g.calls++
	return g.trustworthy, g.reason, g.err
}

// leafScanFake is a PipelineScanner that serves the whole vector index as one
// segment_rebuild page and COUNTS its calls, so a test can assert the resident arm
// issued ZERO scans.
type leafScanFake struct {
	vectors map[string][]byte
	calls   int
	served  bool
}

func (s *leafScanFake) PipelineScan(_ context.Context, _ *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	s.calls++
	if s.served {
		return &knowledgev1.PipelineScanResponse{}, nil // terminating empty page.
	}
	s.served = true
	items := make([]*knowledgev1.PipelineScanItem, 0, len(s.vectors))
	for id, v := range s.vectors {
		items = append(items, &knowledgev1.PipelineScanItem{NodeId: id, BinaryVector: v})
	}
	return &knowledgev1.PipelineScanResponse{Items: items}, nil
}

// leafAttachCorpus is the shared fixture both arms run over: a two-member cluster
// "c1" whose members share a vector, plus a singleton leaf adjacent to one of them
// carrying an identical vector, so the leaf clears the linked gate and attaches.
func leafAttachCorpus() (communityOf map[string]string, commSize map[string]int, adj map[string][]string, vectors map[string][]byte) {
	v := bitVec(0, 1, 2, 3, 4, 5, 6, 7)
	communityOf = map[string]string{"m1": "c1", "m2": "c1", "leaf": "leaf"}
	commSize = map[string]int{"c1": 2, "leaf": 1}
	adj = map[string][]string{"leaf": {"m1"}, "m1": {"leaf", "m2"}, "m2": {"m1"}}
	vectors = map[string][]byte{"m1": v, "m2": v, "leaf": v}
	return communityOf, commSize, adj, vectors
}

// TestLeafAttachment_ResidentResolutionPreferred is the source-switch gate: a
// trustworthy HNSW verdict resolves member vectors IN-PROCESS with ZERO
// PipelineScan calls, a declining verdict falls back to the unchanged server
// drain — and BOTH arms attach the same leaf. The last part is what makes this a
// source test rather than a behavior change: the switch must move where vectors
// come from and nothing else.
func TestLeafAttachment_ResidentResolutionPreferred(t *testing.T) {
	ctx := context.Background()

	t.Run("trustworthy verdict resolves resident with zero scans", func(t *testing.T) {
		communityOf, commSize, adj, vectors := leafAttachCorpus()
		resident := &residentFake{vectors: vectors}
		gate := &gateFakeVerdict{trustworthy: true, reason: "hnsw arm measured and non-degenerate"}
		scanner := &leafScanFake{vectors: vectors}

		p := (&PropagationLoop{gc: &leafProvenanceFake{}, scanner: scanner}).
			WithVectorDeps(resident, gate)
		p.runLeafAttachment(ctx, communityOf, commSize, adj, nil, true)

		assert.Equal(t, 0, scanner.calls,
			"the resident arm must issue ZERO PipelineScan calls — that is the whole point of the seam")
		assert.Positive(t, resident.calls, "vectors came from the resident engines")
		assert.Equal(t, 1, gate.calls, "the coverage gate is probed ONCE per pass")
		assert.Equal(t, "c1", communityOf["leaf"], "the leaf attached to its neighbor's cluster")
	})

	t.Run("declining verdict falls back to the drain and attaches the same leaf", func(t *testing.T) {
		communityOf, commSize, adj, vectors := leafAttachCorpus()
		resident := &residentFake{vectors: vectors}
		gate := &gateFakeVerdict{trustworthy: false, reason: "hnsw arm degenerate"}
		scanner := &leafScanFake{vectors: vectors}

		p := (&PropagationLoop{gc: &leafProvenanceFake{}, scanner: scanner}).
			WithVectorDeps(resident, gate)
		p.runLeafAttachment(ctx, communityOf, commSize, adj, nil, true)

		assert.Positive(t, scanner.calls, "a declining gate falls back to the server drain")
		assert.Zero(t, resident.calls, "and does NOT resolve from a pool it just declined")
		assert.Equal(t, "c1", communityOf["leaf"],
			"the drain arm attaches the SAME leaf — the gate switches the vector SOURCE, not the outcome")
	})

	t.Run("a failed resident read falls back rather than attaching over a partial index", func(t *testing.T) {
		communityOf, commSize, adj, vectors := leafAttachCorpus()
		resident := &residentFake{vectors: vectors, failOn: "m2"}
		gate := &gateFakeVerdict{trustworthy: true, reason: "hnsw arm measured and non-degenerate"}
		scanner := &leafScanFake{vectors: vectors}

		p := (&PropagationLoop{gc: &leafProvenanceFake{}, scanner: scanner}).
			WithVectorDeps(resident, gate)
		p.runLeafAttachment(ctx, communityOf, commSize, adj, nil, true)

		assert.Positive(t, scanner.calls,
			"a resident READ FAILURE takes the drain — attaching over a partially-resolved index would silently veto real candidates")
		assert.Equal(t, "c1", communityOf["leaf"], "and the outcome is still the same attachment")
	})

	t.Run("no seams wired keeps the pre-resident drain behavior", func(t *testing.T) {
		communityOf, commSize, adj, vectors := leafAttachCorpus()
		scanner := &leafScanFake{vectors: vectors}

		p := &PropagationLoop{gc: &leafProvenanceFake{}, scanner: scanner}
		p.runLeafAttachment(ctx, communityOf, commSize, adj, nil, true)

		assert.Positive(t, scanner.calls, "degraded mode drains, exactly as before the resident seam existed")
		assert.Equal(t, "c1", communityOf["leaf"])
	})
}

// leafProvenanceFake answers the ONE bulk provenance edge read with a real
// relates-to edge from the leaf to its neighbor, so the leaf gates at the linked
// (0.60) tier rather than the stricter session-sibling tier.
type leafProvenanceFake struct{}

func (leafProvenanceFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Edges: []*knowledgev1.Edge{
		{Type: "relates-to", FromId: "leaf", ToId: "m1"},
	}}, nil
}

// TestResolveMemberVectors_VectorlessIsNotAnError pins the ok=false contract the
// resident seam inherits from Manager.VectorByID: a node with no stored vector is
// simply ABSENT from the index — the same shape a drained index has for it — and
// never an error that would skip the whole pass.
func TestResolveMemberVectors_VectorlessIsNotAnError(t *testing.T) {
	v := bitVec(0, 1)
	resident := &residentFake{vectors: map[string][]byte{"has-vector": v}}
	gate := &gateFakeVerdict{trustworthy: true, reason: "ok"}
	p := (&PropagationLoop{}).WithVectorDeps(resident, gate)

	idx, source, _, err := p.resolveMemberVectors(context.Background(), []string{"has-vector", "vectorless"}, map[string]string{"has-vector": "has-vector", "vectorless": "vectorless"}, map[string]int{"has-vector": 1, "vectorless": 1}, nil)
	require.NoError(t, err, "a vectorless node must not fail the resolution")
	assert.Equal(t, vectorSourceResident, source)
	assert.Len(t, idx, 1, "only the embedded node is in the index")
	assert.Contains(t, idx, "has-vector")
	assert.NotContains(t, idx, "vectorless", "the vectorless node is absent, to be retried on a later pass")
}
