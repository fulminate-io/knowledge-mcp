// SPDX-License-Identifier: Apache-2.0

package kgtypes

import (
	"testing"
)

// TestSyncEligible_PerType asserts the complement-form predicate: every graph
// type is sync-eligible EXCEPT the raw/LLM-skipped graphs (logs, web, pdf).
// This is the client-side mirror of server store.SkipsLLMProcessing.
//
// The checks row is the one that is easy to get backwards: its LLM-processing
// posture is unusual — the summarizer is off while the embed axis is on — which
// invites an inference in one direction or the other about whether it syncs. It
// does, and neither axis is the reason. Those are independent axes; see
// TestSyncEligible_ChecksAreEligibleDeliberately.
func TestSyncEligible_PerType(t *testing.T) {
	cases := []struct {
		gt   GraphType
		want bool
	}{
		{GraphKnowledge, true},
		{GraphCode, true},
		{GraphCloud, true},
		{GraphCICD, true},
		{GraphPractice, true},
		{GraphLinkage, true},
		{GraphTransformers, true},
		{GraphChecks, true},
		{GraphLogs, false},
		{GraphWebRaw, false},
		{GraphPDFRaw, false},
	}
	for _, tc := range cases {
		if got := SyncEligible(tc.gt); got != tc.want {
			t.Errorf("SyncEligible(%q) = %v, want %v", tc.gt, got, tc.want)
		}
	}
}

// TestSyncEligibleGraphTypes_OrderedSet asserts the eligible set is exactly the
// 8-element ordered set {knowledge, code, cloud, cicd, practice, linkage,
// transformers, checks}, excluding logs/web/pdf, in that order.
//
// WHAT THE checks ROW ASSERTS. Both the mechanical state — SyncEligible is a
// complement predicate and checks is absent from its {logs, web, pdf} exclusion
// set — AND the decision behind it: checks SHOULD sync. It is the compiled half
// of the practice corpus, practice already syncs, and portability is the point
// of compiling prose into a check at all. See
// TestSyncEligible_ChecksAreEligibleDeliberately for the full reasoning and the
// server-side arm this depends on.
func TestSyncEligibleGraphTypes_OrderedSet(t *testing.T) {
	want := []GraphType{
		GraphKnowledge,
		GraphCode,
		GraphCloud,
		GraphCICD,
		GraphPractice,
		GraphLinkage,
		GraphTransformers,
		GraphChecks,
	}
	got := SyncEligibleGraphTypes()
	if len(got) != len(want) {
		t.Fatalf("SyncEligibleGraphTypes() returned %d types %v, want %d %v",
			len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SyncEligibleGraphTypes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSyncEligible_ChecksAreEligibleDeliberately pins the ONE graph type whose
// sync eligibility is a decision rather than a property, and pins it against the
// specific wrong inference a future reader is most likely to make.
//
// THE WRONG INFERENCE, stated so it can be recognized: "checks is not LLM-processed
// — therefore it must not sync either." That reasoning is invalid, and it was
// invalid even while its premise held. Summarization and embedding govern LLM
// spend and ranked-search hygiene; sync governs where the bytes live. They are
// independent axes, and GraphLinkage is the standing precedent for exactly this
// combination: absent from the per-graph opt-in table, absent from
// store.SkipsLLMProcessing, and sync-eligible.
//
// THE PREMISE IS NOW FALSE AS WELL AS IRRELEVANT, and that changes nothing about
// the conclusion. Checks IS enrolled on the embed axis today, with its fixture
// nodes excluded by node type. The wrong inference is kept here in its
// premise-neutral form because the reasoning error — deriving an egress decision
// from an LLM-processing fact — is the thing a future reader has to recognize,
// and it outlives whichever way the premise happens to point.
//
// WHY CHECKS SYNC. Three reasons, none of them accidental:
//  1. Checks are the COMPILED HALF of the practice corpus, derived from practice
//     example nodes. GraphPractice is already sync-eligible, so excluding the
//     derived artifact while its source travels would be anomalous.
//  2. It protects nothing. The corpus the checks come from is already wherever
//     the configured backend puts it; a checks exclusion would not change where
//     any of that data lives.
//  3. Portability IS the value proposition. Compiling prose into a deterministic
//     check pays off precisely because the compiled artifact then runs forever
//     token-free — and only if it travels. Excluding it means every machine
//     re-pays the compile.
//
// THIS IS A PAIR, NOT A LINE. kgtypes.SyncEligible reporting true is only half;
// the server's graphTypeFromSegment must carry a matching
// `case string(store.GraphChecks)` arm or the client pushes what the server
// 400s. Nothing in the build bridges those two modules, so changing either one
// alone re-opens the incoherence in the opposite direction.
//
// FORWARD RISK, recorded because it changes provenance rather than the decision:
// fixtures today come from practice example nodes — book- and web-derived
// teaching snippets, not customer code. Recipe emission of checks is planned. If
// fixtures ever come to be derived from a customer's OWN repository, what syncs
// stops being library-derived content and starts being customer source. That
// would be the moment to revisit this, and it is the reason the reasoning is
// written down rather than left to the complement form's silence.
func TestSyncEligible_ChecksAreEligibleDeliberately(t *testing.T) {
	if !SyncEligible(GraphChecks) {
		t.Error("GraphChecks must be sync-eligible — read this test's doc comment before " +
			"excluding it; not-embedded does not imply not-synced, and the server arm pairs with it")
	}

	// KNOWN-NEGATIVE CONTROL. The assertion above is a positive, and a
	// SyncEligible broken to return true for everything would satisfy it while
	// proving nothing. A type that is genuinely INELIGIBLE must say so in the
	// same run.
	if SyncEligible(GraphLogs) {
		t.Fatal("control: GraphLogs must NOT be sync-eligible — SyncEligible is returning " +
			"true for everything, so the checks assertion above proves nothing")
	}

	// The predicate and the derived ordered set are two surfaces; only the
	// predicate is asserted above. checks must actually appear in the set.
	var found bool
	for _, gt := range SyncEligibleGraphTypes() {
		if gt == GraphChecks {
			found = true
		}
	}
	if !found {
		t.Error("GraphChecks is absent from SyncEligibleGraphTypes() — the predicate and " +
			"the derived set disagree")
	}
}

// TestBuiltinGraphTypeNames_IsTheFullVocabulary pins the accepted graph-selector
// vocabulary. The client's graph-selector refusal renders this list verbatim, so
// a built-in dropped from it becomes a valid selector an error string calls
// invalid — a wrong message no other test would notice, because a stale
// vocabulary inside an error fails nothing on its own.
//
// The expectation is written out from the exported constants rather than read
// back from allGraphTypes: an assertion sourced from the same slice the function
// projects would agree with any drop or reorder by construction.
func TestBuiltinGraphTypeNames_IsTheFullVocabulary(t *testing.T) {
	want := []string{
		"knowledge", "code", "cloud", "cicd", "practice", "linkage",
		"transformers", "checks", "logs", "web", "pdf",
	}
	got := BuiltinGraphTypeNames()
	if len(got) != len(want) {
		t.Fatalf("BuiltinGraphTypeNames() = %v (%d names), want %v (%d names)",
			got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("BuiltinGraphTypeNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Every returned name satisfies the sibling predicate, and a non-built-in
	// never appears. The second half is the control: without it a function
	// returning every string imaginable would satisfy the first.
	for _, name := range got {
		if !IsBuiltinGraphType(name) {
			t.Errorf("BuiltinGraphTypeNames returned %q, which IsBuiltinGraphType rejects", name)
		}
		if name == "all" {
			t.Error(`"all" is not a graph type and must never appear in the vocabulary`)
		}
	}

	// The returned slice is a COPY: mutating it must not reach the package's own
	// state, or one caller's error-message formatting would corrupt the next
	// caller's vocabulary.
	got[0] = "mutated"
	if again := BuiltinGraphTypeNames(); again[0] != "knowledge" {
		t.Errorf("BuiltinGraphTypeNames aliases package state: second call returned %q", again[0])
	}
}
