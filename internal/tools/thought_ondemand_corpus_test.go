// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// chargeHydrateRecorder records EVERY read an on-demand reflect handler issues,
// including ones it was never taught about: the final arm records "UNEXPECTED:..."
// rather than quietly returning a zero value. That is what makes the ZERO assertions
// below mean "no hydrate happened" rather than "no hydrate is observable".
//
// Its classification follows the in-tree counting fake for the propagation pass:
// bucket by return mode and by the REQUESTED EDGE-TYPE SET, and count by-id hydrates
// separately. That fake is a test symbol in another package and cannot be imported,
// so this is the same classification re-authored against the tools-side seam — the
// shape is the reuse, not the code.
type chargeHydrateRecorder struct {
	mu sync.Mutex
	// chargedByThoughtReads counts bulk edge reads filtered to exactly EdgeChargedBy.
	// Every such read in this fixture pivots on thought ids; the charge-pivot split
	// the precedent carries has no analog here because the fixture seeds no
	// evidenced-by edges.
	chargedByThoughtReads int
	// otherEdgeReads counts edge reads over any other edge-type set — counted rather
	// than smeared into the one above.
	otherEdgeReads int
	// events records by-id hydrates and anything unrecognized.
	events []string
	// chargeNode is the WIRE payload served for a by-id hydrate of ch1.
	chargeNode *knowledgev1.Node
	// chargedByEdges is the bulk edge read's payload.
	chargedByEdges []*knowledgev1.Edge
}

func (r *chargeHydrateRecorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, s)
}

func (r *chargeHydrateRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *chargeHydrateRecorder) chargedByCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chargedByThoughtReads
}

func (r *chargeHydrateRecorder) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		r.record("UNEXPECTED:non-query plan")
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		types := q.GetSelection().GetEdgeTypes()
		if len(types) == 1 && types[0] == string(kgtypes.EdgeChargedBy) {
			r.mu.Lock()
			r.chargedByThoughtReads++
			r.mu.Unlock()
			return &knowledgev1.ExecuteResponse{Edges: r.chargedByEdges}, nil
		}
		r.mu.Lock()
		r.otherEdgeReads++
		r.mu.Unlock()
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if ids := q.GetIds(); len(ids) > 0 {
		r.record("hydrate:" + strings.Join(ids, ","))
		resp := &knowledgev1.ExecuteResponse{}
		for _, id := range ids {
			if r.chargeNode != nil && id == r.chargeNode.GetId() {
				resp.Nodes = append(resp.Nodes, r.chargeNode)
			}
		}
		return resp, nil
	}
	r.record("UNEXPECTED:query with no ids and no edge mode")
	return &knowledgev1.ExecuteResponse{}, nil
}

// onDemandCorpusFixture builds the shared fixture: two clustered thoughts in the
// resident snapshot, one charged-by edge on the wire, and the charge node available
// EITHER from the resident charge snapshot (warmCharges) or only by a by-id hydrate.
func onDemandCorpusFixture(warmCharges bool) (interceptTestDeps, *chargeHydrateRecorder) {
	charge := &knowledgev1.Node{Id: "ch1", Type: string(kgtypes.NodeCharge), UpdatedAt: 5}
	rec := &chargeHydrateRecorder{
		chargeNode: charge,
		chargedByEdges: []*knowledgev1.Edge{
			{FromId: "t1", ToId: "ch1", Type: string(kgtypes.EdgeChargedBy)},
		},
	}
	deps := interceptTestDeps{
		gc: rec,
		corpusThoughts: []*knowledgev1.Node{
			{Id: "t1", Type: string(kgtypes.NodeThought), UpdatedAt: 1, Metadata: map[string]string{"cluster_id": "c1"}},
			{Id: "t2", Type: string(kgtypes.NodeThought), UpdatedAt: 2, Metadata: map[string]string{"cluster_id": "c1"}},
		},
	}
	if warmCharges {
		deps.corpusCharges = []*knowledgev1.Node{charge}
	}
	return deps, rec
}

// TestOnDemandReflect_ChargesServedFromResidentSnapshot: with a fully warm source,
// the handler resolves its charges from the resident snapshot and issues NO by-id
// hydrate at all. Before the change the evidence-adjacency leg was handed a nil
// source and forced the hydrate even with a warm cache.
func TestOnDemandReflect_ChargesServedFromResidentSnapshot(t *testing.T) {
	deps, rec := onDemandCorpusFixture(true)

	clusters, _, src := fetchClusterContext(context.Background(), deps)
	require.NotEmpty(t, clusters, "precondition: the warm snapshot produced clusters to work over")
	require.NotNil(t, src, "the memo is returned so the caller can share it")

	for _, e := range rec.recorded() {
		assert.False(t, strings.HasPrefix(e, "hydrate:"),
			"a warm charge snapshot serves every charge resident — no by-id hydrate: %s", e)
		assert.False(t, strings.HasPrefix(e, "UNEXPECTED:"),
			"no read this recorder was never taught about: %s", e)
	}
}

// TestOnDemandReflect_ChargeMapComposedOnce pins the read count by EQUALITY. Before
// the change it was three: the cluster detection, the evidence adjacency and the
// personality scalar pass each composed the thought->charges map independently.
//
// Equality and not <= 1 deliberately: a ZERO would mean the charge map was never
// read at all, and this fixture must not be able to pass that way.
func TestOnDemandReflect_ChargeMapComposedOnce(t *testing.T) {
	deps, rec := onDemandCorpusFixture(true)

	clusters, _, _ := fetchClusterContext(context.Background(), deps)
	require.NotEmpty(t, clusters, "precondition: the handler had clusters to compose charges over")

	assert.Equal(t, 1, rec.chargedByCount(),
		"one handler call issues EXACTLY ONE bulk charged-by read — the three stages share one memo")
}

// TestOnDemandReflect_ChargeHydrateControl_ColdSnapshot is the RECORDER-LIVENESS
// control. HONEST LABEL: it passes both before and after the production change. Its
// only job is to prove the recorder can see a by-id hydrate at all, so the zero in
// the warm test above means "none happened" rather than "none are observable".
func TestOnDemandReflect_ChargeHydrateControl_ColdSnapshot(t *testing.T) {
	deps, rec := onDemandCorpusFixture(false)

	clusters, _, _ := fetchClusterContext(context.Background(), deps)
	require.NotEmpty(t, clusters, "precondition: same fixture, same clusters")

	var hydrates []string
	for _, e := range rec.recorded() {
		if strings.HasPrefix(e, "hydrate:") {
			hydrates = append(hydrates, e)
		}
	}
	require.NotEmpty(t, hydrates, "a COLD charge snapshot must produce an observable by-id hydrate")
	assert.Contains(t, strings.Join(hydrates, "|"), "ch1",
		"the hydrate names the charge the resident snapshot could not serve")
}
