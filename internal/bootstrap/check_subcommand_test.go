// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_subcommand_test.go pins the shell face of the corpus-check
// classification: that `check` is dispatched at all, that the three exit codes
// are distinguishable and each produced by the state it names, and that the CLI
// and the MCP verdict cannot disagree.

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// checkFinding builds one finding with the given title. The title is what the
// classification keys on, so it is the only field that has to be real.
func checkFinding(title string) foundation.Finding {
	return foundation.Finding{Algorithm: corpusscan.AnalyzerName, Title: title}
}

// checkVerdictFixtures is the SHARED table both faces are driven through. It is
// one table rather than two so the cross-face test cannot compare a CLI row
// against an MCP row that describes a different run.
//
// topK IS A ROW PROPERTY EVEN THOUGH THE CLI TAKES NO CAP. The CLI declares no
// -top-k flag, so on that face rendered always equals total and the rendered
// truncated value is identically v.Truncated. A capped row is therefore not
// about the CLI honoring a cap — it is a permanent record of the defect's shape:
// a row whose findings classify DIFFERENTLY once a render-side slice is folded,
// so both faces go red the day either one reintroduces the fold-then-clip order.
func checkVerdictFixtures() []struct {
	name      string
	findings  []foundation.Finding
	topK      int
	wantExit  int
	wantToken string
} {
	return []struct {
		name      string
		findings  []foundation.Finding
		topK      int
		wantExit  int
		wantToken string
	}{
		{"clean", nil, 0, 0, tools.VerdictClean},
		{
			"one flagged site",
			[]foundation.Finding{checkFinding("no-fmt-println at a.go:1")},
			0, ExitCheckFlagged, tools.VerdictFlagged,
		},
		{
			"one refused check",
			[]foundation.Finding{checkFinding(corpusscan.RefusalPrefixUnvalidated + "go:broken")},
			0, ExitCheckInconclusive, tools.VerdictInconclusive,
		},
		{
			"the run ceiling truncated the output",
			[]foundation.Finding{checkFinding(corpusscan.TruncationTitleRun)},
			0, ExitCheckInconclusive, tools.VerdictInconclusive,
		},
		{
			"the per-check ceiling truncated the output",
			[]foundation.Finding{checkFinding(corpusscan.TruncationPrefixCheck + "go:noisy")},
			0, ExitCheckInconclusive, tools.VerdictInconclusive,
		},
		{
			"flagged AND refused reports inconclusive",
			[]foundation.Finding{
				checkFinding("no-fmt-println at a.go:1"),
				checkFinding(corpusscan.RefusalPrefixEnvironment + "go:unplaceable"),
			},
			0, ExitCheckInconclusive, tools.VerdictInconclusive,
		},
		{
			"an llm_only disclosure alone is still clean",
			[]foundation.Finding{{
				Algorithm: corpusscan.AnalyzerName,
				Title:     corpusscan.DisclosureTitleLLMOnly,
				Metrics:   map[string]float64{"llm_only_total": 2},
			}},
			0, 0, tools.VerdictClean,
		},
		{
			// Built disclosure-then-site deliberately, mirroring the analyzer's own
			// lead-first ordering, so a cap of 1 keeps exactly the finding that
			// classifies as neither a site nor a refusal — which is what makes the
			// clipped slice read CLEAN while the true fold reads FLAGGED.
			"a render cap below the finding count moves neither face",
			[]foundation.Finding{
				{
					Algorithm: corpusscan.AnalyzerName,
					Title:     corpusscan.DisclosureTitleLLMOnly,
					Metrics:   map[string]float64{"llm_only_total": 1},
				},
				checkFinding("no-fmt-println at a.go:1"),
			},
			1, ExitCheckFlagged, tools.VerdictFlagged,
		},
		{
			// A scope fact, not a completeness one: a run that reached test
			// files answered the question it was asked, so the disclosure alone
			// classifies CLEAN exactly as the llm_only one does.
			"a test-file disclosure alone is still clean",
			testFilesDisclosureFindings(3),
			0, 0, tools.VerdictClean,
		},
	}
}

// testFilesDisclosureFindings builds the analyzer's test-file disclosure with
// the given count, through the SAME title and metric constants the analyzer
// emits it under. A hand-typed copy here would let this table describe a
// disclosure the fold does not recognize.
func testFilesDisclosureFindings(n int) []foundation.Finding {
	return []foundation.Finding{{
		Algorithm: corpusscan.AnalyzerName,
		Title:     corpusscan.DisclosureTitleTestFiles,
		Metrics:   map[string]float64{corpusscan.MetricTestFilesScanned: float64(n)},
	}}
}

// TestCheckRun_ExitCodesMatchTheVerdictClassification drives the real
// report-and-map path for each state and asserts the exit code it produces.
//
// THE INCONCLUSIVE ARM IS THE FALSIFYING ONE. An implementation that collapsed
// refused into flagged passes the clean and flagged rows and fails here, which is
// the entire reason the two codes are separate: a gate that cannot tell a real
// finding from a probe that could not run lets an author read a refused corpus as
// a caught defect.
func TestCheckRun_ExitCodesMatchTheVerdictClassification(t *testing.T) {
	for _, tc := range checkVerdictFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			// The REAL path: reportCheckRun picks the sentinel, subcommandExit maps
			// it. Asserting on the sentinel alone would leave the mapping untested,
			// and the mapping is where a code collision would live.
			code, _ := subcommandExit(reportCheckRun(tc.findings))
			assert.Equal(t, tc.wantExit, code)
		})
	}

	// THE CODES DO NOT COLLIDE with the two every subcommand already returns.
	// Without this, 3 and 4 could drift onto 1 or 2 and every row above would
	// still pass while the gate became unreadable.
	assert.NotEqual(t, 1, ExitCheckFlagged, "3 must not collide with the generic-failure code")
	assert.NotEqual(t, 2, ExitCheckFlagged, "3 must not collide with the no-valid-session code")
	assert.NotEqual(t, 1, ExitCheckInconclusive, "4 must not collide with the generic-failure code")
	assert.NotEqual(t, 2, ExitCheckInconclusive, "4 must not collide with the no-valid-session code")
	assert.NotEqual(t, ExitCheckFlagged, ExitCheckInconclusive,
		"flagged and inconclusive must be distinguishable, which is the whole reason there are two")

	// KNOWN-POSITIVE CONTROL on the mapper itself: a plain error still maps to 1,
	// so the codes above come from the sentinels rather than from a mapper that
	// returns something unusual for everything.
	generic, _ := subcommandExit(assert.AnError)
	assert.Equal(t, 1, generic, "an ordinary error must still be a generic failure")
}

// TestCheckRun_AgreesWithTheMCPVerdictOnTheSameFindings is the behavioral form of
// the one-source-of-truth rule.
//
// WHY BEHAVIORAL AND NOT A GREP. An absence gate over "the CLI does not mention
// the title constants" needs a survivor list and still cannot see a
// re-implementation written with different tokens. What matters is that the two
// faces AGREE, so the same findings are driven through the CLI's exit path and
// the MCP's token and the answers are required to correspond — a CLI that
// re-implemented the classification identically today would pass, and would go
// red the moment either side's rule moved.
func TestCheckRun_AgreesWithTheMCPVerdictOnTheSameFindings(t *testing.T) {
	// The correspondence the two faces are claimed to share, written once.
	tokenForExit := map[int]string{
		0:                     tools.VerdictClean,
		ExitCheckFlagged:      tools.VerdictFlagged,
		ExitCheckInconclusive: tools.VerdictInconclusive,
	}

	sawEachToken := map[string]bool{}
	for _, tc := range checkVerdictFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := subcommandExit(reportCheckRun(tc.findings))
			token := tools.RunVerdictToken(corpusscan.ClassifyRun(tc.findings))
			sawEachToken[token] = true

			require.Contains(t, tokenForExit, code, "the CLI produced an exit code no verdict token corresponds to")
			assert.Equal(t, tokenForExit[code], token,
				"the CLI exit status and the MCP verdict token must classify the same findings identically")
			// Pinned against the table's own expectation too, so the two faces
			// agreeing on a WRONG answer is not a pass.
			assert.Equal(t, tc.wantToken, token)
			assert.Equal(t, tc.wantExit, code)

			// WITHOUT THIS CONTROL THE CAPPED ROW IS DECORATION. The CLI takes no
			// cap, so agreement on a capped row is otherwise satisfied by any
			// implementation at all. The control pins that the row is one where
			// folding over a render-side slice WOULD have answered differently —
			// which is the defect's exact shape, and what makes the row go red the
			// day either face reintroduces it.
			if tc.topK > 0 {
				clipped := tools.RunVerdictToken(corpusscan.ClassifyRun(
					foundation.TruncateTopK(tc.findings, tc.topK)))
				require.NotEqual(t, token, clipped,
					"a capped row must be one where the clipped slice classifies differently, or it discriminates nothing")
			}
		})
	}

	// KNOWN-POSITIVE CONTROL: the table exercised all three tokens. Agreement over
	// a table that only ever produced CLEAN would be satisfied by two faces that
	// both always say clean.
	for _, token := range []string{tools.VerdictClean, tools.VerdictFlagged, tools.VerdictInconclusive} {
		assert.True(t, sawEachToken[token], "the shared table must drive the %s verdict at least once", token)
	}
}

// TestRunSubcommand_CheckIsDispatchedAndUnknownVerbIsRefused asserts the
// subcommand is CLAIMED rather than falling through.
//
// FALLING THROUGH IS THE FAILURE MODE, not merely an omission: an unclaimed
// `check` reaches the flag parser, which rejects it as an unknown flag and exits
// 2 — colliding with the no-valid-session code, so a criterion would read "this
// binary is too old" as "you are logged out".
func TestRunSubcommand_CheckIsDispatchedAndUnknownVerbIsRefused(t *testing.T) {
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })

	os.Args = []string{"knowledge", "check", "bogus"}
	handled, code := RunSubcommand()
	require.True(t, handled, "RunSubcommand must CLAIM `check` rather than let it reach the flag parser")
	assert.Equal(t, 1, code, "an unknown verb is an ordinary usage failure, not a verdict")

	// The refusal names both halves: what was wrong and what is admitted.
	err := runCheckVerb([]string{"bogus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"bogus"`, "the refusal must name the offending verb")
	assert.Contains(t, err.Error(), "run", "the refusal must enumerate the admitted set")

	// A missing verb is refused the same way rather than defaulting to run.
	missing := runCheckVerb(nil)
	require.Error(t, missing)
	assert.Contains(t, missing.Error(), "run")

	// KNOWN-POSITIVE CONTROL on the dispatch: a subcommand this switch does NOT
	// know is still unclaimed, so handled==true above is a property of `check`
	// rather than of a switch that claims everything.
	os.Args = []string{"knowledge", "no-such-subcommand"}
	unhandled, _ := RunSubcommand()
	assert.False(t, unhandled, "an unknown subcommand must fall through, or the control proves nothing")
}
