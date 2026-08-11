// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// TestTypedUpdate_PostMergeMetadataDrivesSummary pins the SOURCE the typed
// update's auto-summary is derived from: the metadata the SAME call is about to
// store, never the node's pre-merge stored value. A derive that reads pre-merge
// state embeds the value the update is replacing, so a caller who changes a
// derive source sees a summary describing the old one.
//
// Every fixture routes its new value through the `metadata` map rather than the
// first-class param — that is the route a param-only derive is blind to, and a
// param-routed edit derives correctly either way (guarded separately as a
// known-positive control). Old and new values never share a string, so a stale
// pass-through cannot be mistaken for a correct re-derivation.
//
// Expected values are built by CALLING the shared derivers, never by
// hand-concatenating the format, which is single-sourced in projects/derive.go.
//
// The fixture commands are `go build`-shaped on purpose: the criterion
// command-shape guard only inspects commands containing the `go test` token, and
// a fixture carrying a bare test selector would be rejected before the derive
// ran, failing these subtests for an unrelated reason.
func TestTypedUpdate_PostMergeMetadataDrivesSummary(t *testing.T) {
	cases := []struct {
		name string
		node *knowledgev1.Node
		args mutateArgs
		want string
	}{
		{
			name: "criterion command via metadata map",
			node: nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
				map[string]string{"type": "automated", "command": "go build ./old/..."}),
			args: mutateArgs{Operation: "update", ID: "c1",
				Metadata: map[string]string{"command": "go build ./new/..."}},
			want: projects.DeriveCriterionSummary("automated", "the suite is green", "go build ./new/..."),
		},
		{
			name: "criterion type via metadata map",
			node: nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
				map[string]string{"type": "manual", "command": "go build ./x/..."}),
			args: mutateArgs{Operation: "update", ID: "c1",
				Metadata: map[string]string{"type": "automated"}},
			want: projects.DeriveCriterionSummary("automated", "the suite is green", "go build ./x/..."),
		},
		{
			name: "rule scope via metadata map",
			node: nodeOf(t, "r1", "rule", "no naked goroutines", "no naked goroutines",
				map[string]string{"scope": "*.go"}),
			args: mutateArgs{Operation: "update", ID: "r1",
				Metadata: map[string]string{"scope": "*.ts"}},
			want: projects.DeriveRuleSummary("no naked goroutines", "*.ts"),
		},
		{
			name: "finding evidence via metadata map",
			node: nodeOf(t, "f1", "finding", "leak", "leak in handler",
				map[string]string{"evidence": "store.go:42"}),
			args: mutateArgs{Operation: "update", ID: "f1",
				Metadata: map[string]string{"evidence": "store.go:99"}},
			want: projects.DeriveFindingSummary("leak in handler", "store.go:99"),
		},
		{
			// An explicit empty metadata value is a real CLEAR, so the derive must
			// test key PRESENCE rather than non-emptiness: falling back to the stored
			// value here would re-derive the summary from the scope just cleared. The
			// equality below is the assertion — the want carries no scope suffix at all.
			name: "explicit metadata clear drops the derived suffix",
			node: nodeOf(t, "r1", "rule", "no naked goroutines", "no naked goroutines",
				map[string]string{"scope": "*.go"}),
			args: mutateArgs{Operation: "update", ID: "r1",
				Metadata: map[string]string{"scope": ""}},
			want: projects.DeriveRuleSummary("no naked goroutines", ""),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, handled := runTypedUpdate(t, tc.node, tc.args)
			require.True(t, handled, "a typed update on a derive-routing type must be claimed")
			m := lastUpdatePlan(t, fc)
			assert.Equal(t, tc.want, m.GetSetFields()["summary"],
				"the summary must be derived from the metadata this call stores, not the pre-merge value")
		})
	}
}

// TestTypedUpdate_PostMergeAudit_DerivationsUnchanged fences the derivations the
// post-merge audit found to have NO defect, so correcting the metadata-routed
// ones cannot disturb them. All three subtests are CHARACTERIZATION GUARDS —
// green both before and after the derive-source correction, never red-first.
func TestTypedUpdate_PostMergeAudit_DerivationsUnchanged(t *testing.T) {
	// CATCHER: an implementation reading the caller's metadata map directly rather
	// than the MERGED map would satisfy every metadata-routed assertion above and
	// silently regress every caller using the first-class command param.
	t.Run("first-class param still wins over the stored value", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "automated", "command": "go build ./old/..."})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
			Command: "go build ./new/..."})
		require.True(t, handled)
		m := lastUpdatePlan(t, fc)
		assert.Equal(t,
			projects.DeriveCriterionSummary("automated", "the suite is green", "go build ./new/..."),
			m.GetSetFields()["summary"])
	})

	// The criterion name mirror reads the supplied description only — it has no
	// metadata route. CATCHER: a derive that started stamping name off the merged
	// map would overwrite every criterion's displayed label on a command-only edit.
	t.Run("criterion name mirrors description only when description is supplied", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "automated", "command": "go build ./old/..."})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
			Metadata: map[string]string{"command": "go build ./new/..."}})
		require.True(t, handled)
		assert.NotContains(t, lastUpdatePlan(t, fc).GetSetFields(), "name",
			"a command-only edit must not re-stamp the criterion name")

		fc2, handled2 := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
			Description: "the new observable check"})
		require.True(t, handled2)
		assert.Equal(t, "the new observable check", lastUpdatePlan(t, fc2).GetSetFields()["name"])
	})

	// The explicit-summary escape hatch: create REJECTS a caller-supplied summary
	// on a criterion, update ACCEPTS one. The derive — and everything added to it —
	// lives inside the summary == "" branch, which an explicit summary never
	// enters, so the hatch holds by construction. This subtest keeps it that way
	// for the metadata route (the param route is covered in the sibling file).
	t.Run("explicit summary still wins over post-merge derivation", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "automated", "command": "go build ./old/..."})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
			Summary:  "my explicit summary",
			Metadata: map[string]string{"command": "go build ./new/..."}})
		require.True(t, handled)
		assert.Equal(t, "my explicit summary", lastUpdatePlan(t, fc).GetSetFields()["summary"])
	})
}

// TestTypedUpdate_DerivedSummaryLengthGuard pins the length gate on the DERIVED
// criterion summary: an over-cap derivation rejects loudly with zero forwards,
// mirroring what the create path already enforces on the same derivation.
//
// The gate is deliberately asymmetric and each fence below holds one side of it.
// It covers the DERIVED summary only — an explicit caller-supplied summary is
// passed through verbatim and unvalidated at any length, because it lives
// outside the derive branch the gate sits in. It is criterion-scoped, because a
// criterion summary is derived-not-authored while rule and finding summaries are
// author-supplied and their derivations are routinely over-cap.
//
// Fixtures are built with strings.Repeat and assert their intended rune count in
// the test, so a fixture cannot silently drift off the boundary it exists to pin.
func TestTypedUpdate_DerivedSummaryLengthGuard(t *testing.T) {
	// The only red-first subtest here. The derived summary runs 573 runes: the
	// 21-rune type prefix, a 400-rune description, " (", the 149-rune new command
	// and ")". Note the command ITSELF is under the cap — only the derivation is
	// over it, so a gate keying on the command's own length would not fire.
	t.Run("oversized derived summary rejects with zero forwards", func(t *testing.T) {
		desc := strings.Repeat("d", 400)
		newCommand := "go build ./new/... " + strings.Repeat("x", 130)
		require.Equal(t, 573, utf8.RuneCountInString(
			projects.DeriveCriterionSummary("automated", desc, newCommand)))

		node := nodeOf(t, "c1", "criterion", desc, desc,
			map[string]string{"type": "automated", "command": "go build ./old/..."})
		a := mutateArgs{Operation: "update", ID: "c1",
			Metadata: map[string]string{"command": newCommand}}
		fc := &fakeGraphCaller{}
		deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
		handled, res := handleClientMutateUpdateTyped(context.Background(), deps,
			withRawArgs(a, typedUpdateRaw(t, a)), node)

		require.True(t, handled, "the rejection is a claim — it must not fall through silently")
		require.True(t, res.IsError, "an over-cap DERIVED summary must reject: %s", toolResultText(res))
		body := toolResultText(res)
		assert.Contains(t, body, "criterion.summary", "the rejection must name the field path")
		assert.Contains(t, body, "auto-derived", "the rejection must say the summary was derived")
		// This proves zero forwards were ISSUED. It does not prove the stored node
		// is unchanged — a fake caller has no stored node.
		assert.Empty(t, fc.execMutations, "a rejected update issues ZERO forwards")
	})

	// CATCHER: the summary helper used for AUTHOR-supplied text word-boundary
	// TRUNCATES and reports success. Against such a regression the caller gets a
	// valid 500-rune prefix and no error, so "no error" alone passes — the length
	// equality is the assertion whose failure names the defect.
	t.Run("explicit oversized caller summary still lands", func(t *testing.T) {
		explicit := strings.Repeat("s", 581)
		node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "automated", "command": "go build ./old/..."})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
			Summary:  explicit,
			Metadata: map[string]string{"command": "go build ./new/..."}})
		require.True(t, handled)
		forwarded := lastUpdatePlan(t, fc).GetSetFields()["summary"]
		assert.Equal(t, explicit, forwarded, "an explicit summary passes through verbatim")
		assert.Equal(t, 581, utf8.RuneCountInString(forwarded),
			"an explicit summary must never be truncated — it is unvalidated at any length")
	})

	// BOUNDARY CONTROL: exactly at the cap is accepted. Without it a gate that
	// rejected AT the cap would be indistinguishable from a correct one.
	t.Run("derived summary exactly at the cap is accepted", func(t *testing.T) {
		desc := strings.Repeat("d", 464)
		want := projects.DeriveCriterionSummary("automated", desc, "go build ./x")
		require.Equal(t, 500, utf8.RuneCountInString(want))

		node := nodeOf(t, "c1", "criterion", desc, desc,
			map[string]string{"type": "automated", "command": "go build ./old/..."})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
			Metadata: map[string]string{"command": "go build ./x"}})
		require.True(t, handled)
		assert.Equal(t, want, lastUpdatePlan(t, fc).GetSetFields()["summary"])
	})

	// SCOPE FENCE: finding summaries are author-supplied, their create path never
	// validates a derived value, and a long description makes essentially every
	// evidence-only update derive over the cap. Gating them would reject all of
	// those, so this proves the gate is criterion-scoped rather than blanket.
	t.Run("an over-cap finding derivation is not gated", func(t *testing.T) {
		desc := strings.Repeat("f", 600)
		want := projects.DeriveFindingSummary(desc, "store.go:42")
		require.Greater(t, utf8.RuneCountInString(want), 500)

		node := nodeOf(t, "f1", "finding", "leak", desc, nil)
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "f1",
			Evidence: "store.go:42"})
		require.True(t, handled)
		assert.Equal(t, want, lastUpdatePlan(t, fc).GetSetFields()["summary"])
	})

	// ROUTE FENCE: a 75-rune command through the FIRST-CLASS param derives to 499,
	// one under the cap. CATCHER: this goes red if the gate keys on the ROUTE a
	// value arrived by rather than on the derived length.
	t.Run("a long in-range param-route command is accepted", func(t *testing.T) {
		desc := strings.Repeat("d", 400)
		paramCommand := "go build ./new/... " + strings.Repeat("x", 56)
		want := projects.DeriveCriterionSummary("automated", desc, paramCommand)
		require.Equal(t, 499, utf8.RuneCountInString(want))

		node := nodeOf(t, "c1", "criterion", desc, desc,
			map[string]string{"type": "automated", "command": "go build ./old/..."})
		fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
			Command: paramCommand})
		require.True(t, handled)
		assert.Equal(t, want, lastUpdatePlan(t, fc).GetSetFields()["summary"])
	})
}
