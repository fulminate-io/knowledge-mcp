// SPDX-License-Identifier: Apache-2.0

package kgtypes

import "testing"

// raw_graph_residency_test.go pins the SPLIT between two residency questions that
// used to be one predicate: which graphs carry rebuildable search segments, and
// which graphs may be pushed to the cloud.
//
// HasRebuildableSegments used to be written as SyncEligible-minus-two, so the two
// answers could not disagree. That made "carries segments AND never leaves the
// machine" — exactly what a raw graph needs — inexpressible.

// setOf builds a membership set from a predicate over every builtin type, so the
// assertions below compare a DERIVED set against a fixture-authored expectation
// rather than comparing one predicate's output against the other's.
func setOf(pred func(GraphType) bool) map[GraphType]bool {
	out := map[GraphType]bool{}
	for _, gt := range allGraphTypes {
		if pred(gt) {
			out[gt] = true
		}
	}
	return out
}

func assertSet(t *testing.T, label string, got map[GraphType]bool, want []GraphType) {
	t.Helper()
	wantSet := map[GraphType]bool{}
	for _, gt := range want {
		wantSet[gt] = true
		if !got[gt] {
			t.Errorf("%s: %q is MISSING from the set", label, gt)
		}
	}
	for gt := range got {
		if !wantSet[gt] {
			t.Errorf("%s: %q is in the set and must not be", label, gt)
		}
	}
	// Compared against the FIXTURE-authored length, never against the other
	// predicate's: two sets that lost the same member are still equal.
	if len(got) != len(wantSet) {
		t.Errorf("%s: set has %d members, want %d (%v)", label, len(got), len(wantSet), want)
	}
}

// TestRawGraphResidency_SegmentsWithoutSync is the property pair plus the three
// controls that catch the ways a lazy implementation would pass.
func TestRawGraphResidency_SegmentsWithoutSync(t *testing.T) {
	// THE PROPERTY: segments without sync — the combination the old derivation
	// could not express, and the reason this file exists.
	for _, gt := range []GraphType{GraphWebRaw, GraphPDFRaw} {
		if !HasRebuildableSegments(gt) {
			t.Errorf("HasRebuildableSegments(%q) must be true — a raw graph holds local search segments", gt)
		}
		if SyncEligible(gt) {
			t.Errorf("SyncEligible(%q) must be false — a raw graph is a temporary scratch corpus, dropped once a golden graph is produced", gt)
		}
	}

	// CONTROL 1, the one that matters: linkage is sync-eligible WITH NO segments.
	// This is the only assertion here that fails if HasRebuildableSegments is ever
	// rewritten to derive from SyncEligible again — a collapse into a synonym
	// would satisfy every other line in this file.
	//
	// IT USED TO BE transformers, which carried the same combination until the
	// family was removed. Linkage is the surviving member of it: proxy edges with
	// no text, so an embedding-gated rebuild scan finds nothing, while its bytes
	// are still eligible to leave the machine.
	if !SyncEligible(GraphLinkage) {
		t.Error("control: linkage must stay SYNC-ELIGIBLE — the split moved segment residency only")
	}
	if HasRebuildableSegments(GraphLinkage) {
		t.Error("control: linkage must have NO rebuildable segments — it holds proxy edges and no text, " +
			"so an embedding-gated rebuild scan finds nothing for it")
	}

	// CONTROL 2 and 3: a type true on both axes and a type false on both, so a
	// predicate that had broken into a constant could not satisfy the disagreement
	// above, and a blanket widening is caught rather than rewarded.
	if !SyncEligible(GraphKnowledge) || !HasRebuildableSegments(GraphKnowledge) {
		t.Fatal("control: knowledge must be true on BOTH axes")
	}
	if SyncEligible(GraphLogs) || HasRebuildableSegments(GraphLogs) {
		t.Fatal("control: logs must be false on BOTH axes")
	}

	// The two full sets, each compared against a fixture-authored expectation.
	assertSet(t, "HasRebuildableSegments", setOf(HasRebuildableSegments), []GraphType{
		GraphKnowledge, GraphCode, GraphCloud, GraphCICD,
		GraphPractice, GraphChecks, GraphWebRaw, GraphPDFRaw,
	})
	assertSet(t, "SyncEligible", setOf(SyncEligible), []GraphType{
		GraphKnowledge, GraphCode, GraphCloud, GraphCICD,
		GraphPractice, GraphLinkage, GraphChecks,
	})

	// SyncEligibleGraphTypes IS THE SURFACE sync(operation:"list") READS, so it is
	// walked separately: the displayed set and the enforced predicate could drift.
	for _, gt := range SyncEligibleGraphTypes() {
		if gt == GraphWebRaw || gt == GraphPDFRaw {
			t.Errorf("SyncEligibleGraphTypes() lists %q — sync(list) would offer an operator a graph that must never leave the machine", gt)
		}
	}
	if len(SyncEligibleGraphTypes()) != 7 {
		t.Errorf("SyncEligibleGraphTypes() has %d members, want 7 — the enrollment must not have moved sync residency",
			len(SyncEligibleGraphTypes()))
	}
}
