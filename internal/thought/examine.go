// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// ChargeDetail holds a charge node with its linked evidence.
type ChargeDetail struct {
	Charge   *knowledgev1.Node
	Evidence []*knowledgev1.Node
}

// ConnectionDetail holds a connected node with edge metadata.
type ConnectionDetail struct {
	Node      *knowledgev1.Node
	EdgeType  kgtypes.EdgeType
	Direction string // "outgoing" or "incoming"
}

// ThoughtExamination is a comprehensive view of a single thought.
type ThoughtExamination struct {
	Node        *knowledgev1.Node
	Properties  ThoughtProperties
	Charges     []ChargeDetail
	Connections []ConnectionDetail
	SessionName string
}

// ExamineThought returns a comprehensive view of a thought with all charges
// and connections. FUL-247 client-side: every store.Store().Query call from
// the original pkg/thought/query.go ExamineThought is translated into one
// bulk wire round-trip. Per-evidence and per-connection hydration goes
// through fetchNodesByIDs so the BCN4 v2 perf invariant holds.
func ExamineThought(ctx context.Context, gc *graphclient.GraphClient, thoughtID string) (ThoughtExamination, error) {
	if gc == nil {
		return ThoughtExamination{}, errors.New("thought: ExamineThought: graph client unavailable")
	}
	if thoughtID == "" {
		return ThoughtExamination{}, errors.New("thought: ExamineThought: thoughtID is required")
	}

	node, ok := fetchNode(ctx, gc, thoughtID)
	if !ok {
		return ThoughtExamination{}, fmt.Errorf("thought not found: %s", thoughtID)
	}

	// Charges + evidence: one charges_for round-trip for the thought, then
	// one fetchNodesByIDs round-trip over the union of all evidence IDs.
	chargeMap := chargeMapForThoughts(ctx, gc, []string{thoughtID})
	chargeNodes := chargeMap[thoughtID]
	props := computePropertiesFromCharges(chargeNodes)

	// Gather all per-charge evidence IDs in one traversal per charge, then
	// bulk-hydrate. Per-charge traverse calls are unavoidable — but they
	// only execute when the thought actually has charges, and within each
	// charge the evidence hydration is a single bulk round-trip.
	allEvidenceIDs := make([]string, 0, len(chargeNodes))
	chargeEvidence := make(map[string][]string, len(chargeNodes))
	for _, c := range chargeNodes {
		evIDs, _ := fetchEdgeNeighborsTyped(ctx, gc, c.Id, kgtypes.EdgeEvidencedBy, true)
		chargeEvidence[c.Id] = evIDs
		allEvidenceIDs = append(allEvidenceIDs, evIDs...)
	}
	evNodeMap := fetchNodesByIDs(ctx, gc, allEvidenceIDs)
	charges := make([]ChargeDetail, 0, len(chargeNodes))
	for _, c := range chargeNodes {
		var evidence []*knowledgev1.Node
		for _, eid := range chargeEvidence[c.Id] {
			if en, ok := evNodeMap[eid]; ok {
				evidence = append(evidence, en)
			}
		}
		charges = append(charges, ChargeDetail{Charge: c, Evidence: evidence})
	}

	// Connections — one edges-for-node round-trip; collect peer IDs and
	// bulk-hydrate.
	outgoing, incoming, _ := fetchEdgesForNode(ctx, gc, thoughtID)
	peerIDs := make([]string, 0, len(outgoing)+len(incoming))
	for i := range outgoing {
		e := &outgoing[i]
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeChargedBy {
			continue // already enumerated in charges
		}
		peerIDs = append(peerIDs, e.ToId)
	}
	for i := range incoming {
		peerIDs = append(peerIDs, incoming[i].FromId)
	}
	peerMap := fetchNodesByIDs(ctx, gc, peerIDs)

	var connections []ConnectionDetail
	for i := range outgoing {
		e := &outgoing[i]
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeChargedBy {
			continue
		}
		if peer, ok := peerMap[e.ToId]; ok {
			connections = append(connections, ConnectionDetail{Node: peer, EdgeType: kgtypes.EdgeType(e.Type), Direction: "outgoing"})
		}
	}
	for i := range incoming {
		e := &incoming[i]
		if peer, ok := peerMap[e.FromId]; ok {
			connections = append(connections, ConnectionDetail{Node: peer, EdgeType: kgtypes.EdgeType(e.Type), Direction: "incoming"})
		}
	}

	return ThoughtExamination{
		Node:        node,
		Properties:  props,
		Charges:     charges,
		Connections: connections,
		SessionName: kgtypes.Value(node, "session"),
	}, nil
}
