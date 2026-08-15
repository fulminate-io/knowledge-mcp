// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
)

// TestAugmentWithPreciseCallGraph_SummaryEmittedOnEveryPath proves the summary
// line is emitted when the augmentation DEGRADES, not only when it succeeds.
//
// This is the property the seven-key source grep cannot see. The three early
// returns — no go.mod, a BuildGoCallGraph error, an empty call map — used to
// skip the line entirely, which is how 94 consecutive launchd collects degraded
// to pure tree-sitter with no summary at all. A source grep for the keys stays
// green against exactly that code, because the keys are present on the ONE line
// the degraded path never reaches.
//
// The augmented arm is the KNOWN POSITIVE. Without it a probe pointed at the
// wrong logger, a handler that swallowed everything, and a genuinely emitted
// line would all look identical, and the degraded arm's assertion would prove
// nothing.
func TestAugmentWithPreciseCallGraph_SummaryEmittedOnEveryPath(t *testing.T) {
	const summaryMsg = "precise call graph: replaced tree-sitter CALLS edges"

	t.Run("degraded_no_go_mod", func(t *testing.T) {
		// A tree with no go.mod takes the first early return.
		out := captureSlog(t, func() {
			augmentWithPreciseCallGraph(t.Context(), parser.PopulateResult{}, t.TempDir())
		})
		requireContains(t, out, summaryMsg,
			"the degraded path returned without emitting the one-per-collect summary")
		requireContains(t, out, "modules=0", "the degraded summary must report zero modules analyzed")
		requireContains(t, out, "go_toolchain_missing=false",
			"the degraded summary must carry the toolchain key so an operator can tell the two degraded causes apart")
	})

	t.Run("augmented_known_positive", func(t *testing.T) {
		root := writeCoverageFixture(t)
		pop, err := parser.Populate(t.Context(), "cov", root)
		require.NoError(t, err)

		out := captureSlog(t, func() {
			augmentWithPreciseCallGraph(t.Context(), pop, root)
		})
		requireContains(t, out, summaryMsg, "the augmented path emitted no summary at all")
		// The fixture has a root module and a nested one, so a working module
		// discovery reports two — a value the degraded arm above can never produce.
		requireContains(t, out, "modules=2",
			"the augmented summary must report the module count discovery actually found")
		requireContains(t, out, "kept_uncovered_go=1",
			"the augmented summary must report the build-tag-excluded file's preserved edge")
	})
}

// captureSlog runs fn with the default logger redirected into a buffer and
// returns everything it wrote. The previous default is restored before it
// returns, so neighboring tests keep their logging.
func captureSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// requireContains fails the test naming the missing substring, because a raw
// "does not contain" on a multi-kilobyte log dump is unreadable.
func requireContains(t *testing.T, haystack, needle, why string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("want: the captured log to contain %q; got: it does not — %s", needle, why)
	}
}
