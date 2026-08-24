// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// manifest_diff_test.go — the catchers for the deletion formula and for shadow
// mode's ONLY deliverable, its divergence logging.

func hashOf(b byte) [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = b
	}
	return h
}

func manifestOf(entries map[string][32]byte) *knowledgev1.CollectManifestResponse {
	resp := &knowledgev1.CollectManifestResponse{ManifestId: "mid"}
	for path, h := range entries {
		hh := h
		resp.Entries = append(resp.Entries, &knowledgev1.ManifestEntry{
			FilePath: path, ContributionHash: hh[:],
		})
	}
	return resp
}

// TestDeletions_UnchangedFilesAreNotNamed bounds OVER-naming: a re-collect where
// every file is unchanged names ZERO deletions.
//
// It is a SEPARATE top-level test from its sibling deliberately. Folded together,
// an implementation that names zero deletions in BOTH cases — the signature of a
// permanently inert deletion feature — still passes whichever half runs first.
func TestDeletions_UnchangedFilesAreNotNamed(t *testing.T) {
	present := map[string][32]byte{
		"pkg/a.go": hashOf(1), "pkg/b.go": hashOf(2), "cmd/main.go": hashOf(3),
	}
	d := computeCollectDiff(manifestOf(present), present)
	require.Empty(t, d.changedFiles, "precondition: nothing changed")
	require.Len(t, d.unchangedFiles, 3, "precondition: every file is verified present")

	// Nothing was chunked, because nothing changed — which is exactly the shape
	// the naive manifest-minus-chunked formula would read as "everything deleted".
	got := deletionSet(d.manifestFiles, nil, d.unchangedFiles)
	require.Empty(t, got,
		"an unchanged re-collect must name ZERO deletions: unchanged files are verified present, "+
			"they are simply not re-uploaded")
}

// TestDeletions_DeletedFileIsNamedExactly bounds UNDER-naming: a re-collect after
// removing one file names exactly that path, and the directories that lost their
// last file — and nothing else.
func TestDeletions_DeletedFileIsNamedExactly(t *testing.T) {
	manifest := map[string][32]byte{
		"pkg/a.go": hashOf(1), "pkg/b.go": hashOf(2), "gone/only.go": hashOf(3),
	}
	// gone/only.go is removed; the other two are unchanged.
	unchanged := []string{"pkg/a.go", "pkg/b.go"}

	got := deletionSet(manifest, nil, unchanged)
	require.Contains(t, got, "gone/only.go", "the removed file must be named")
	require.Contains(t, got, "gone",
		"the directory that lost its LAST file must be named too — its package node is otherwise immortal")
	require.NotContains(t, got, "pkg", "a directory with surviving files must NEVER be named")
	require.NotContains(t, got, ".", "the repo root survives while any file does")
	require.NotContains(t, got, "pkg/a.go")
	require.NotContains(t, got, "pkg/b.go")
	require.Len(t, got, 2, "exactly the removed file and its emptied directory")
}

// TestDirsOf_SegmentBoundaries pins the boundary rule the whole directory leg
// rests on. A raw string-prefix test would call "a/b" an ancestor of "a/bc/d.go"
// and name a live sibling directory as deleted.
func TestDirsOf_SegmentBoundaries(t *testing.T) {
	got := dirsOf([]string{"a/b/c.go"})
	require.Contains(t, got, "a/b")
	require.Contains(t, got, "a")
	require.Contains(t, got, ".", "the repo root is rendered as the literal dot")

	sibling := dirsOf([]string{"a/bc/d.go"})
	require.Contains(t, sibling, "a/bc")
	require.NotContains(t, sibling, "a/b",
		"control: a/b is NOT an ancestor of a/bc/d.go — segment boundaries, not string prefixes")
}

// TestShadowMode_UploadsFullSetAndNoDeletions pins shadow mode's contract: it
// computes the diff and then uploads EVERYTHING and sends NOTHING.
//
// The diffModeOn arm is the KNOWN-POSITIVE CONTROL: without it, "shadow sends no
// deletions" is indistinguishable from a decision function that never returns
// deletions in any mode.
func TestShadowMode_UploadsFullSetAndNoDeletions(t *testing.T) {
	present := map[string][32]byte{"pkg/a.go": hashOf(1), "pkg/b.go": hashOf(9)}
	manifest := manifestOf(map[string][32]byte{"pkg/a.go": hashOf(1), "gone.go": hashOf(4)})
	d := computeCollectDiff(manifest, present)
	deletions := deletionSet(d.manifestFiles, d.changedFiles, d.unchangedFiles)
	require.NotEmpty(t, deletions, "precondition: this fixture DOES produce a deletion")

	// filelessChanged is passed FALSE throughout, which is the value that could
	// wrongly suppress the fileless set: only the diffModeOn arm may honor it, and
	// the two non-diff arms must keep the set regardless.
	shadow := decideUpload(diffModeShadow, d, deletions, false)
	require.True(t, shadow.uploadAll, "shadow uploads the FULL set exactly as today")
	require.Empty(t, shadow.deletions, "shadow sends NO deletions, however many it computed")
	require.True(t, shadow.keepFileless,
		"shadow uploads the FULL set, and the fileless half is part of it — a decline outside diff mode "+
			"would send a narrowed payload under a mode whose whole contract is that it sends everything")

	// CONTROL: the same diff under diffModeOn does send them, so the emptiness
	// above is evidence about shadow rather than about the decision function.
	on := decideUpload(diffModeOn, d, deletions, false)
	require.False(t, on.uploadAll)
	require.Equal(t, deletions, on.deletions,
		"control: with the diff armed the same computed set IS sent")
	require.False(t, on.keepFileless,
		"control: with the diff armed an UNCHANGED fileless signature does decline the set — otherwise "+
			"the two assertions above are about a field that is always true")
	onChanged := decideUpload(diffModeOn, d, deletions, true)
	require.True(t, onChanged.keepFileless,
		"control: a CHANGED fileless signature keeps the set even under diff mode")

	off := decideUpload(diffModeOff, d, deletions, false)
	require.True(t, off.uploadAll, "off is today's behavior")
	require.Empty(t, off.deletions)
	require.True(t, off.keepFileless, "off is today's behavior for the fileless half too")
}

// captureErrorLogs swaps the default slog handler for one writing to a buffer and
// returns the accumulated output.
func captureErrorLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prior) })
	fn()
	return buf.String()
}

// TestShadowMode_DivergenceClasses is shadow mode's real gate: its entire
// deliverable is this logging, and "uploads full, sends no deletions" gates none
// of it.
//
// EACH POSITIVE SUBTEST ALSO ASSERTS THE OTHER TWO CLASSES ARE ABSENT. That is
// what makes "one distinct message per class" a gated property: an implementation
// emitting one generic line for every class fails every positive subtest, because
// the line it emits also matches the two the subtest requires absent.
func TestShadowMode_DivergenceClasses(t *testing.T) {
	// The distinguishing fragment of each class's message.
	const (
		fragHash       = "contribution hash DISAGREES"
		fragManifest   = "names files this collect never discovered"
		fragDiscovered = "discovered files absent from a populated manifest"
	)
	allFrags := []string{fragHash, fragManifest, fragDiscovered}

	emit := func(present map[string][32]byte, manifest map[string][32]byte) string {
		return captureErrorLogs(t, func() {
			d := computeCollectDiff(manifestOf(manifest), present)
			for class, paths := range shadowDivergences(d) {
				logShadowDivergence(class, paths)
			}
		})
	}
	requireOnly := func(t *testing.T, out, want string) {
		t.Helper()
		require.Contains(t, out, want)
		for _, other := range allFrags {
			if other == want {
				continue
			}
			require.NotContains(t, out, other,
				"each class must have its OWN message — a generic line would match this too")
		}
	}

	t.Run("hash_mismatch", func(t *testing.T) {
		out := emit(map[string][32]byte{"pkg/a.go": hashOf(1)}, map[string][32]byte{"pkg/a.go": hashOf(2)})
		requireOnly(t, out, fragHash)
	})
	t.Run("manifest_only", func(t *testing.T) {
		out := emit(map[string][32]byte{"pkg/a.go": hashOf(1)},
			map[string][32]byte{"pkg/a.go": hashOf(1), "ghost.go": hashOf(7)})
		requireOnly(t, out, fragManifest)
	})
	t.Run("discovered_only", func(t *testing.T) {
		out := emit(map[string][32]byte{"pkg/a.go": hashOf(1), "fresh.go": hashOf(5)},
			map[string][32]byte{"pkg/a.go": hashOf(1)})
		requireOnly(t, out, fragDiscovered)
	})
	t.Run("none", func(t *testing.T) {
		// THE NEGATIVE HALF. Without it, an implementation that logs all three
		// classes unconditionally on every collect passes the other three.
		out := emit(map[string][32]byte{"pkg/a.go": hashOf(1)}, map[string][32]byte{"pkg/a.go": hashOf(1)})
		require.Empty(t, strings.TrimSpace(out), "an agreeing collect logs NOTHING")
	})
}
