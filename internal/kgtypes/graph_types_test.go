// SPDX-License-Identifier: Apache-2.0

package kgtypes

import (
	"testing"
)

// TestSyncEligible_PerType asserts the complement-form predicate: every graph
// type is sync-eligible EXCEPT the raw/LLM-skipped graphs (logs, web, pdf).
// This is the client-side mirror of server store.SkipsLLMProcessing.
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
// 7-element ordered set {knowledge, code, cloud, cicd, practice, linkage,
// transformers}, excluding logs/web/pdf, in that order.
func TestSyncEligibleGraphTypes_OrderedSet(t *testing.T) {
	want := []GraphType{
		GraphKnowledge,
		GraphCode,
		GraphCloud,
		GraphCICD,
		GraphPractice,
		GraphLinkage,
		GraphTransformers,
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
