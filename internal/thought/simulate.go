// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// SimulatedChange shows the before/after properties for a thought.
type SimulatedChange struct {
	ThoughtID string
	Before    ThoughtProperties
	After     ThoughtProperties
}

// SimulationResult shows the impact of a hypothetical change.
type SimulationResult struct {
	AffectedThoughts []SimulatedChange
	Description      string
}

// SimulateRemoveCharge previews the impact of removing a charge without
// committing. FUL-247 client-side: takes a graph client; bulk-fetches all
// affected thought charges in one round-trip via chargeMapForThoughts.
func SimulateRemoveCharge(ctx context.Context, gc Caller, chargeID string) (SimulationResult, error) {
	if gc == nil {
		return SimulationResult{}, errors.New("simulate: SimulateRemoveCharge: graph client unavailable")
	}

	charge, ok := fetchNode(ctx, gc, chargeID)
	if !ok {
		return SimulationResult{}, fmt.Errorf("charge not found: %s: %w", chargeID, graphclient.ErrNotFound)
	}
	if kgtypes.NodeType(charge.Type) != kgtypes.NodeCharge {
		return SimulationResult{}, fmt.Errorf("node %s is not a charge (type: %s)", chargeID, charge.Type)
	}

	thoughtIDs, _ := fetchEdgeNeighborsTyped(ctx, gc, chargeID, kgtypes.EdgeChargedBy, false)
	if len(thoughtIDs) == 0 {
		return SimulationResult{}, fmt.Errorf("charge %s has no parent thought", chargeID)
	}

	result := SimulationResult{
		Description: fmt.Sprintf("Remove charge %s (%s, weight %s): %s",
			chargeID, kgtypes.Value(charge, "polarity"), kgtypes.Value(charge, "weight"), charge.SymbolName),
	}

	// One bulk charges fetch for all affected thoughts (BCN4 v2 perf
	// invariant — never per-thought N+1).
	chargeMap := chargeMapForThoughts(ctx, gc, thoughtIDs)

	for _, tid := range thoughtIDs {
		charges := chargeMap[tid]
		before := computePropertiesFromCharges(charges)
		after := computePropertiesExcluding(charges, chargeID)

		result.AffectedThoughts = append(result.AffectedThoughts, SimulatedChange{
			ThoughtID: tid,
			Before:    before,
			After:     after,
		})
	}

	return result, nil
}

// SimulateInvalidateThought previews the cascading impact of invalidating a
// thought. FUL-247 client-side: takes a graph client; bulk-fetches neighbor
// nodes + charges in two round-trips total (one outgoing-targets, one
// fetchNodesByIDs, one chargeMapForThoughts over the thought-typed
// subset).
func SimulateInvalidateThought(ctx context.Context, gc Caller, thoughtID string) (SimulationResult, error) {
	if gc == nil {
		return SimulationResult{}, errors.New("simulate: SimulateInvalidateThought: graph client unavailable")
	}

	node, ok := fetchNode(ctx, gc, thoughtID)
	if !ok {
		return SimulationResult{}, fmt.Errorf("thought not found: %s: %w", thoughtID, graphclient.ErrNotFound)
	}

	result := SimulationResult{
		Description: fmt.Sprintf("Invalidate thought: %s", node.SymbolName),
	}

	// Bulk-fetch root + neighbors charges in one round-trip.
	neighborIDs, _ := fetchOutgoingTargets(ctx, gc, thoughtID)
	allIDs := append([]string{thoughtID}, neighborIDs...)
	allMap := fetchNodesByIDs(ctx, gc, neighborIDs)

	chargeIDs := []string{thoughtID}
	for _, nid := range neighborIDs {
		if n, ok := allMap[nid]; ok && kgtypes.NodeType(n.Type) == kgtypes.NodeThought {
			chargeIDs = append(chargeIDs, nid)
		}
	}
	chargeMap := chargeMapForThoughts(ctx, gc, chargeIDs)

	before := computePropertiesFromCharges(chargeMap[thoughtID])
	result.AffectedThoughts = append(result.AffectedThoughts, SimulatedChange{
		ThoughtID: thoughtID,
		Before:    before,
		After:     ThoughtProperties{Valence: -1.0, SelfTrust: baseSelfTrust}, // invalidated = fully negative
	})

	for _, nid := range neighborIDs {
		n, ok := allMap[nid]
		if !ok || kgtypes.NodeType(n.Type) != kgtypes.NodeThought {
			continue
		}
		nBefore := computePropertiesFromCharges(chargeMap[nid])
		result.AffectedThoughts = append(result.AffectedThoughts, SimulatedChange{
			ThoughtID: nid,
			Before:    nBefore,
			After:     nBefore, // full propagation needed for accurate after; show same for now
		})
	}

	_ = allIDs // retained for parity with the original; the slice is informational
	return result, nil
}

// SimulateAddCharge previews the impact of adding a hypothetical charge.
// FUL-247 client-side: one fetchNode + one chargeMapForThoughts round-trip.
func SimulateAddCharge(ctx context.Context, gc Caller, thoughtID, polarity string, weight float64) (SimulationResult, error) {
	if gc == nil {
		return SimulationResult{}, errors.New("simulate: SimulateAddCharge: graph client unavailable")
	}

	node, ok := fetchNode(ctx, gc, thoughtID)
	if !ok {
		return SimulationResult{}, fmt.Errorf("thought not found: %s: %w", thoughtID, graphclient.ErrNotFound)
	}

	chargeMap := chargeMapForThoughts(ctx, gc, []string{thoughtID})
	before := computePropertiesFromCharges(chargeMap[thoughtID])

	after := before
	w := weight
	switch polarity {
	case "positive":
		after.PositiveWeight += w
	case "negative":
		after.NegativeWeight += w
	default:
		return SimulationResult{}, fmt.Errorf("invalid polarity: %s", polarity)
	}
	after.ChargeCount++

	total := after.PositiveWeight + after.NegativeWeight
	if total > 0 {
		after.Valence = (after.PositiveWeight - after.NegativeWeight) / total
		after.Magnitude = logPlus1(total)
		maxSide := maxf(after.PositiveWeight, after.NegativeWeight)
		minSide := minf(after.PositiveWeight, after.NegativeWeight)
		after.Consistency = 1 - (minSide / maxSide)
	}
	after.SelfTrust = baseSelfTrust + after.Consistency*logPlus1(float64(after.ChargeCount))

	return SimulationResult{
		Description: fmt.Sprintf("Add %s charge (weight %.1f) to: %s", polarity, weight, node.SymbolName),
		AffectedThoughts: []SimulatedChange{{
			ThoughtID: thoughtID,
			Before:    before,
			After:     after,
		}},
	}, nil
}

// RunSimulation dispatches to the appropriate simulation based on action
// type. Mirrors pkg/thought/simulate.go:151-162 with `gc` plumbed through
// each branch.
func RunSimulation(ctx context.Context, gc Caller, action, target, polarity string, weight float64) (SimulationResult, error) {
	switch action {
	case "remove_charge":
		return SimulateRemoveCharge(ctx, gc, target)
	case "invalidate_thought":
		return SimulateInvalidateThought(ctx, gc, target)
	case "add_charge":
		return SimulateAddCharge(ctx, gc, target, polarity, weight)
	default:
		return SimulationResult{}, fmt.Errorf("unknown simulation action: %s", action)
	}
}

// computePropertiesExcluding computes thought properties while excluding a
// specific charge. FUL-247 signature change: now takes a pre-loaded charges
// slice instead of issuing GetChargesForThought internally. Caller is
// expected to have bulk-fetched via chargeMapForThoughts upstream.
func computePropertiesExcluding(charges []*knowledgev1.Node, excludeChargeID string) ThoughtProperties {
	var props ThoughtProperties
	for _, c := range charges {
		if c.Id == excludeChargeID {
			continue
		}
		props.ChargeCount++
		w := parseFloat(kgtypes.Value(c, "weight"))
		switch kgtypes.Value(c, "polarity") {
		case "positive":
			props.PositiveWeight += w
		case "negative":
			props.NegativeWeight += w
		}
	}

	total := props.PositiveWeight + props.NegativeWeight
	if total > 0 {
		props.Valence = (props.PositiveWeight - props.NegativeWeight) / total
		props.Magnitude = logPlus1(total)
		maxSide := maxf(props.PositiveWeight, props.NegativeWeight)
		minSide := minf(props.PositiveWeight, props.NegativeWeight)
		props.Consistency = 1 - (minSide / maxSide)
	}
	props.SelfTrust = baseSelfTrust + props.Consistency*logPlus1(float64(props.ChargeCount))

	return props
}

func logPlus1(x float64) float64 {
	return math.Log(1 + x)
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
