// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_reflect_dispatch.go resolves which of the ten reflect arms
// interceptQueryReflect (thought.go) will take for a given payload, and runs the
// param-accounting gate for it.
//
// WHY A DISPATCHER RATHER THAN TEN INLINE GATE CALLS. thought.go sits close to
// the repo's 500-line file convention and interceptQueryReflect has ten claim
// points; three lines of gate at each would breach it. Hoisting the mapping here
// leaves interceptQueryReflect with ONE call, and keeps the mode-to-armID
// mapping beside nothing else so it is easy to check against the switch it
// mirrors.
//
// THE MAPPING MIRRORS interceptQueryReflect's OWN DISPATCH, in its order: the
// mode switch first, then the recall-routing predicate. Both must stay in step —
// TestQueryArmGateCallSites_BijectWithRegistry catches an arm that loses its
// gate, and the Phase-5 parity harness catches one wired to the wrong armID.
//
// A DECLINE IS NOT A REJECTION. reflectArmFor returns ok=false for every payload
// the reflect surface does not claim (an unrecognized mode carrying no thought
// filter), and accountReflectArm then returns nil so interceptQueryReflect falls
// through to the next claimant exactly as before. Gating a call this surface
// does not serve would reject params on behalf of an arm that never runs.
//
// ONE APPROXIMATION, stated because it is the only place the mapping is not
// exact. mode:"examine" resolves to armReflectThoughtExamine here, but
// interceptQueryReflect only KEEPS that claim once it has fetched the node and
// found it is a NodeThought; a non-thought id declines to the general examine
// arm downstream. The gate therefore runs armReflectThoughtExamine's cells for
// an examine payload that will end up on armExamine. That is harmless rather
// than merely tolerated: the two arms carry IDENTICAL classifications (graph,
// mode, id and format consumed, fields ignored, everything else rejected), so no
// payload can be accepted by one and rejected by the other.
// TestReflectExamineArmsClassifyIdentically pins that equality, so a future edit
// to either arm that would make the approximation observable fails loudly.

import "encoding/json"

// reflectArmFor reports which reflect arm interceptQueryReflect will take, or
// ok=false when the reflect surface declines the payload.
func reflectArmFor(a queryReflectArgs) (armID, bool) {
	switch a.Mode {
	case "personality":
		return armReflectPersonality, true
	case "influence":
		return armReflectInfluence, true
	case "tensions":
		return armReflectTensions, true
	case "blind_spots":
		return armReflectBlindSpots, true
	case "summary":
		return armReflectSummary, true
	case "evolution":
		return armReflectEvolution, true
	case "clusters":
		return armReflectClusters, true
	case "examine":
		// Only an id-bearing examine can reach the thought branch; without one
		// the intercept declines before reading anything else.
		if a.ID == "" {
			return "", false
		}
		return armReflectThoughtExamine, true
	case "simulate":
		return armReflectSimulate, true
	}
	// The recall route, term-identical to thought.go's predicate: mode
	// timeline/charges, any of the six thought-graph properties, or a status
	// filter on the thought corpus.
	recallModes := map[string]bool{"timeline": true, "charges": true}
	hasThoughtFilter := a.ValenceMin != nil || a.ValenceMax != nil || a.MagnitudeMin != nil ||
		a.ConsistMax != nil || a.Session != "" || a.ConnectedTo != ""
	statusOnThoughtCorpus := a.Status != "" &&
		(a.Type == "" || a.Type == "thought" || a.Type == "all")
	if hasThoughtFilter || statusOnThoughtCorpus || recallModes[a.Mode] {
		return armReflectRecall, true
	}
	return "", false
}

// accountReflectArm runs the param-accounting gate for whichever reflect arm the
// payload resolves to. It returns nil — never a rejection — for a payload the
// reflect surface declines, so the chain proceeds unchanged.
func accountReflectArm(a queryReflectArgs, raw json.RawMessage) error {
	arm, ok := reflectArmFor(a)
	if !ok {
		return nil
	}
	return accountQueryParams(arm, raw)
}
