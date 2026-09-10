// SPDX-License-Identifier: Apache-2.0

// manage_checks_verdict_roundtrip_test.go — the verdict line's reader and its
// writer are one contract.
//
// WHY THIS EXISTS. The CLI face no longer performs the scan: it asks the daemon
// to run manage_checks and maps the machine-readable token of that answer onto
// an exit status. That makes renderRunVerdict a WIRE FORMAT with a consumer, and
// a format with a consumer needs a round trip — otherwise a change to the line
// would leave the shell face reading nothing and reporting an unknown corpus for
// every run, which no test of either side alone would catch.

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// TestRenderRunVerdict_RoundTripsThroughTheParser drives every verdict state
// through the real renderer and requires the real parser to read the same token
// back.
func TestRenderRunVerdict_RoundTripsThroughTheParser(t *testing.T) {
	// The three states, each built from findings the analyzer's own fold
	// classifies — not from a hand-set struct, which would let the row assert a
	// verdict the classifier cannot produce.
	for _, tc := range []struct {
		name     string
		findings []foundation.Finding
		want     string
	}{
		{"clean", nil, VerdictClean},
		{
			"flagged",
			[]foundation.Finding{{Algorithm: corpusscan.AnalyzerName, Title: "no-fmt-println at a.go:1"}},
			VerdictFlagged,
		},
		{
			"inconclusive",
			[]foundation.Finding{{Algorithm: corpusscan.AnalyzerName, Title: corpusscan.RefusalPrefixUnvalidated + "go:broken"}},
			VerdictInconclusive,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := corpusscan.ClassifyRun(tc.findings)
			require.Equal(t, tc.want, RunVerdictToken(v), "the row must describe the verdict the fold actually makes")

			rendered := renderRunVerdict(v, len(tc.findings), len(tc.findings))
			token, ok := ParseRunVerdict(rendered)
			require.True(t, ok, "the parser must read the line the renderer wrote: %q", rendered)
			assert.Equal(t, tc.want, token)
		})
	}

	// A CLIPPED RENDER STILL CARRIES A READABLE TOKEN. The clip appends a second
	// line, so a parser that read the whole body rather than the first line would
	// break exactly here.
	clipped := renderRunVerdict(corpusscan.ClassifyRun(
		[]foundation.Finding{{Algorithm: corpusscan.AnalyzerName, Title: "no-fmt-println at a.go:1"}}), 1, 4)
	token, ok := ParseRunVerdict(clipped)
	require.True(t, ok, "a clipped render must still be readable: %q", clipped)
	assert.Equal(t, VerdictFlagged, token)

	// KNOWN-NEGATIVE CONTROL: text that is not this line is REFUSED rather than
	// read as clean, so the round trips above are the parser working and not a
	// parser that says yes to everything.
	for _, notAVerdict := range []string{"", "manage_checks run: repo is required", "corpus_scan: checks_flagged=0", "corpus_scan: MAYBE"} {
		_, readable := ParseRunVerdict(notAVerdict)
		assert.False(t, readable, "%q must not read as a verdict", notAVerdict)
	}
}
