// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestRecommend_NamesEveryUnavailableCause drives all four shapes an empty recommendation
// set can take and asserts each one says why. The claim under test is not "a reason is set"
// — it is that no path leaves a BARE null: an empty result the caller has to explain to
// itself.
//
// The fifth arm is what keeps the field falsifiable in the other direction. Without it, an
// implementation that set a reason unconditionally would pass every other arm.
func TestRecommend_NamesEveryUnavailableCause(t *testing.T) {
	ctx := context.Background()
	report := &DetectorReport{
		DuplicateCommands: []DuplicateCommandRow{{SessionID: "S1", ToolName: "Bash", RunCount: 2}},
	}

	t.Run("config not loaded", func(t *testing.T) {
		got := recommendWith(ctx, report, nil, nil)
		assert.True(t, strings.HasPrefix(got.Unavailable, "config-not-loaded: "),
			"got %q", got.Unavailable)
		assert.NotNil(t, got.Recommendations, "an empty array, never a nil slice")
		assert.Empty(t, got.Recommendations)
	})

	t.Run("synthesizer build error", func(t *testing.T) {
		got := recommendWith(ctx, report, nil, errors.New("distinctive-build-failure-7f3a"))
		assert.True(t, strings.HasPrefix(got.Unavailable, "synthesizer-unavailable: "),
			"got %q", got.Unavailable)
		assert.Contains(t, got.Unavailable, "distinctive-build-failure-7f3a",
			"the underlying error text is carried through, not replaced by the prefix alone")
	})

	t.Run("generate error", func(t *testing.T) {
		fake := llm.NewFakeClient(nil)
		fake.SetError(errors.New("Prompt is too long distinctive-oversize-c91d"))
		got := recommendWith(ctx, report, &Synthesizer{client: fake, model: "test-model"}, nil)

		assert.True(t, strings.HasPrefix(got.Unavailable, "synthesis-failed: "), "got %q", got.Unavailable)
		assert.Contains(t, got.Unavailable, "distinctive-oversize-c91d",
			"the generate error's own text reaches the caller, which is the whole point: it "+
				"was previously visible only in the daemon log")
	})

	t.Run("successful but empty", func(t *testing.T) {
		fake := llm.NewFakeClient(&llm.Response{Content: `{"recommendations":[]}`})
		got := recommendWith(ctx, report, &Synthesizer{client: fake, model: "test-model"}, nil)

		assert.NotNil(t, got.Recommendations, "an empty array, never a nil slice that marshals to null")
		assert.Empty(t, got.Recommendations)
		assert.NotEmpty(t, got.Unavailable, "a successful-but-empty result still names its emptiness")
	})

	t.Run("successful with recommendations", func(t *testing.T) {
		fake := llm.NewFakeClient(&llm.Response{
			Content: `{"recommendations":[{"title":"t","category":"c","impact":"high","rationale":"r"}]}`,
		})
		got := recommendWith(ctx, report, &Synthesizer{client: fake, model: "test-model"}, nil)

		require.Len(t, got.Recommendations, 1)
		assert.Empty(t, got.Unavailable, "a reason that is always set could not pass this arm")
	})
}

// TestRecommend_ReturnsDetectorReportOnEveryDegrade pins the deterministic core: whatever
// happens to synthesis, the detector report still reaches the caller. Recommend is driven
// end to end here — not recommendWith — so the wiring between the two is covered rather
// than assumed.
func TestRecommend_ReturnsDetectorReportOnEveryDegrade(t *testing.T) {
	svc := buildGoldenCorpus(t)

	report, recs, err := svc.Recommend(context.Background(), Filters{})
	require.NoError(t, err, "a degraded synthesis is not an error")
	require.NotNil(t, report, "the deterministic report is returned regardless")
	assert.NotEmpty(t, report.SubagentWallTime, "and it is the real report, not a zero value")
	assert.NotEmpty(t, recs.Unavailable,
		"no LLM is configured in the test process, so the empty result names that cause")
	assert.NotNil(t, recs.Recommendations)
}
