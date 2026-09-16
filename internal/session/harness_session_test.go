// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"testing"
)

// TestHarnessSessionContextRoundTrip pins the accessor T2's account selection
// reads: what is stamped comes back, field for field, including an UNRESOLVED
// verdict — the `none` source and its reason are exactly what a consumer must
// see, so they travel like any other resolution.
func TestHarnessSessionContextRoundTrip(t *testing.T) {
	for _, want := range []HarnessSession{
		{ID: "sess-1", Source: HarnessSourceHeader},
		{ID: "sess-2", Source: HarnessSourceClaudeHook, Cwd: "/w", TranscriptPath: "/t.jsonl"},
		{ID: "sess-3", Source: HarnessSourceCodexMeta, Reason: ReasonCodexHookInert},
		{Source: HarnessSourceNone, Reason: ReasonNoHeader + "; " + ReasonNoMeta},
	} {
		got := HarnessSessionFromContext(ContextWithHarnessSession(context.Background(), want))
		if got != want {
			t.Errorf("round trip = %+v, want %+v", got, want)
		}
	}
}

// TestHarnessSessionFromContext_Unstamped: a context that never ran through the
// resolver reports `none` with ReasonNotResolved, NOT a zero struct. An empty
// Source would read to a consumer as a fifth, undocumented state.
func TestHarnessSessionFromContext_Unstamped(t *testing.T) {
	got := HarnessSessionFromContext(context.Background())
	if got.Source != HarnessSourceNone {
		t.Errorf("source = %q, want %q", got.Source, HarnessSourceNone)
	}
	if got.Reason != ReasonNotResolved {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonNotResolved)
	}
	if got.ID != "" {
		t.Errorf("id = %q, want empty", got.ID)
	}
}

// TestHarnessSessionResolved is the predicate T2 fails on. An id with no source,
// a source with no id, and the `none` verdict are all UNRESOLVED: a consumer
// that requires an identity must not act on any of them.
func TestHarnessSessionResolved(t *testing.T) {
	for _, tc := range []struct {
		name string
		hs   HarnessSession
		want bool
	}{
		{name: "header", hs: HarnessSession{ID: "a", Source: HarnessSourceHeader}, want: true},
		{name: "claude hook", hs: HarnessSession{ID: "a", Source: HarnessSourceClaudeHook}, want: true},
		{name: "codex hook", hs: HarnessSession{ID: "a", Source: HarnessSourceCodexHook}, want: true},
		{name: "codex meta", hs: HarnessSession{ID: "a", Source: HarnessSourceCodexMeta}, want: true},
		{name: "none", hs: HarnessSession{Source: HarnessSourceNone, Reason: ReasonNoMeta}},
		{name: "none carrying an id anyway", hs: HarnessSession{ID: "a", Source: HarnessSourceNone}},
		{name: "a source with no id", hs: HarnessSession{Source: HarnessSourceHeader}},
		{name: "the zero value", hs: HarnessSession{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.hs.Resolved(); got != tc.want {
				t.Errorf("Resolved() = %v, want %v for %+v", got, tc.want, tc.hs)
			}
		})
	}
}
