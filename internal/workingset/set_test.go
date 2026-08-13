// SPDX-License-Identifier: Apache-2.0

package workingset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestNormalize_KnowledgeDefaultAndBranchStrip pins both normalization rules and
// the refusal. The branch case is the one that would fail silently if it broke:
// a branch-qualified member can never equal a registered collector's name or a
// reconcile ref, so the gate would be permanently inert rather than loud.
func TestNormalize_KnowledgeDefaultAndBranchStrip(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		gt       kgtypes.GraphType
		instance string
		want     Ref
		wantOK   bool
	}{
		{
			name: "the knowledge empty instance collapses to default",
			gt:   kgtypes.GraphKnowledge, instance: "",
			want: Ref{GraphType: kgtypes.GraphKnowledge, Name: "default"}, wantOK: true,
		},
		{
			name: "the knowledge default instance is already normal",
			gt:   kgtypes.GraphKnowledge, instance: "default",
			want: Ref{GraphType: kgtypes.GraphKnowledge, Name: "default"}, wantOK: true,
		},
		{
			name: "a branch overlay is stripped to the bare repo name",
			gt:   kgtypes.GraphCode, instance: "repo@some-branch",
			want: Ref{GraphType: kgtypes.GraphCode, Name: "repo"}, wantOK: true,
		},
		{
			name: "a bare repo name is already normal",
			gt:   kgtypes.GraphCode, instance: "repo",
			want: Ref{GraphType: kgtypes.GraphCode, Name: "repo"}, wantOK: true,
		},
		{
			name: "a code target with no instance name resolves nothing",
			gt:   kgtypes.GraphCode, instance: "",
			want: Ref{}, wantOK: false,
		},
		{
			name: "a cloud account is carried through unchanged",
			gt:   kgtypes.GraphCloud, instance: "acct-1",
			want: Ref{GraphType: kgtypes.GraphCloud, Name: "acct-1"}, wantOK: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Normalize(tc.gt, tc.instance)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}

	// The two spellings must be the SAME member, not two — the drift this rule
	// exists to prevent.
	s := New()
	require.True(t, s.Admit(kgtypes.GraphKnowledge, "", "test"))
	assert.False(t, s.Admit(kgtypes.GraphKnowledge, "default", "test"),
		`knowledge/"" and knowledge/"default" must be one member, not two`)
	assert.Equal(t, []Ref{{GraphType: kgtypes.GraphKnowledge, Name: "default"}}, s.Members())

	// Likewise a repo and its overlay.
	require.True(t, s.Admit(kgtypes.GraphCode, "repo@feature", "test"))
	assert.True(t, s.Has(kgtypes.GraphCode, "repo"),
		"an overlay admission must admit the bare repo the collectors are registered under")
}

// TestSet_AdmitIsFirstOnlyAndNilSafe pins the log-once contract and the
// default-deny direction of a nil *Set. The live Set is exercised FIRST so the
// nil assertions cannot pass against a probe that never worked.
func TestSet_AdmitIsFirstOnlyAndNilSafe(t *testing.T) {
	t.Parallel()

	s := New()
	// Registered BEFORE the admission, the way a consumer wires it at startup. Two
	// waiters, because several loops consume this and each must get its own signal.
	wakeA, wakeB := s.Wake(), s.Wake()

	assert.True(t, s.Admit(kgtypes.GraphCode, "repoA", "search"),
		"the first admission of a Ref must report true so the caller logs once")
	assert.False(t, s.Admit(kgtypes.GraphCode, "repoA", "search"),
		"a repeat admission must report false")
	assert.True(t, s.Has(kgtypes.GraphCode, "repoA"))
	assert.False(t, s.Has(kgtypes.GraphCode, "repoB"), "an untouched graph is not a member")
	assert.Equal(t, []Ref{{GraphType: kgtypes.GraphCode, Name: "repoA"}}, s.Members())

	// A refused target admits nothing rather than admitting an empty name.
	assert.False(t, s.Admit(kgtypes.GraphCode, "", "search"))
	assert.Len(t, s.Members(), 1)

	// The first admission signals EVERY registered waiter; the control above
	// proves the same Set really does admit, so an unsignalled channel would be a
	// real failure. Both are asserted because one shared channel would let the
	// first receiver swallow the signal the second loop needed.
	select {
	case <-wakeA:
	default:
		t.Fatal("the first admission must signal the first waiter")
	}
	select {
	case <-wakeB:
	default:
		t.Fatal("the first admission must signal EVERY waiter, not just the first")
	}

	var nilSet *Set
	assert.False(t, nilSet.Admit(kgtypes.GraphCode, "repoA", "search"),
		"a nil Set admits nothing")
	assert.False(t, nilSet.Has(kgtypes.GraphCode, "repoA"),
		"a nil Set is EMPTY, never unrestricted")
	assert.Nil(t, nilSet.Members())
	assert.Nil(t, nilSet.Wake())
}

// TestWakeFor_OnlyTheNamedGraphSignals pins the filtered registration: a WakeFor
// waiter hears the first admission of the graph it named and nothing else, while
// an unfiltered Wake waiter registered beside it still hears every admission.
//
// The unfiltered waiter is the KNOWN-POSITIVE CONTROL for every "did not
// receive" assertion here: on the code-graph admission it proves the Set really
// did signal, so a silent filtered channel means the filter worked rather than
// that nothing was ever wired. Both spellings of the knowledge instance are
// registered because they must resolve to ONE filter, not two.
func TestWakeFor_OnlyTheNamedGraphSignals(t *testing.T) {
	t.Parallel()

	s := New()
	// All registered BEFORE any admission, the way a consumer wires at startup.
	anyGraph := s.Wake()
	forKnowledge := s.WakeFor(kgtypes.GraphKnowledge, "default")
	forKnowledgeEmpty := s.WakeFor(kgtypes.GraphKnowledge, "")
	forOtherRepo := s.WakeFor(kgtypes.GraphCode, "repoB")

	require.True(t, s.Admit(kgtypes.GraphCode, "repoA", "collect"))
	assert.True(t, signaled(anyGraph),
		"the unfiltered waiter must hear a code-graph admission")
	assert.False(t, signaled(forKnowledge),
		"a code-graph admission must NOT wake a waiter filtered to knowledge/default")
	assert.False(t, signaled(forKnowledgeEmpty),
		"a code-graph admission must NOT wake a waiter filtered to knowledge/\"\"")
	assert.False(t, signaled(forOtherRepo),
		"a filtered waiter must not hear a DIFFERENT instance of its own graph type")

	require.True(t, s.Admit(kgtypes.GraphKnowledge, "", "search"))
	assert.True(t, signaled(forKnowledge),
		`an admission spelled "" must signal the waiter registered as "default"`)
	assert.True(t, signaled(forKnowledgeEmpty),
		`an admission spelled "" must signal the waiter registered as "" — one member, one filter`)
	assert.True(t, signaled(anyGraph),
		"the unfiltered waiter still hears every admission")
	assert.False(t, signaled(forOtherRepo),
		"the code-graph waiter must not hear a knowledge admission")

	// A repeat admission is not a FIRST admission, so it signals nobody — the
	// property the filtered registration must not quietly change.
	assert.False(t, s.Admit(kgtypes.GraphKnowledge, "default", "search"))
	assert.False(t, signaled(forKnowledge),
		"a repeat admission is not a first admission and must signal no waiter")
	assert.False(t, signaled(anyGraph),
		"a repeat admission must not signal the unfiltered waiter either")

	// A name Normalize refuses, and a nil *Set, register nothing: the nil channel
	// blocks forever in a select, the correct default-deny when nothing can ever
	// be admitted.
	assert.Nil(t, s.WakeFor(kgtypes.GraphCode, ""),
		"a target that names no concrete graph instance registers no waiter")
	var nilSet *Set
	assert.Nil(t, nilSet.WakeFor(kgtypes.GraphKnowledge, "default"),
		"a nil Set can never admit, so its filtered waiter blocks forever")
}

// signaled reports whether ch carries a pending wake, without blocking. A nil
// channel reports false, which is what a never-registrable waiter must look like.
func signaled(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
