// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_subcommand_files_test.go — the FILE-LIST scope on the SHELL face.
//
// A scope reachable from the MCP tool and not from here is a silent drop between
// two faces of one analyzer: a lane scanning its own diff through the CLI would
// get a whole-tree answer while the same call through the tool got the diff. The
// parameter-accounting test proves the flag is CLASSIFIED; these rows prove it
// reaches the run itself, carrying every path the caller wrote.
//
// THE ROWS MOVED WITH THE FACE. The scope used to be asserted on the analyzer's
// Extra map, which is what the CLI built when it ran the scan in-process; it now
// asserts the manage_checks ARGUMENTS the daemon is asked with, which is where
// the same scope lives on the routed path. Same property, at the seam the
// production code actually crosses.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
)

// checkRunTestRepo is the resolved tree the argument-building rows pass. The
// resolution itself is exercised by the routing test, which drives the real verb.
const checkRunTestRepo = "/abs/checkout"

// TestCheckRunFiles_IsRepeatableAndReachesTheRun pins the flag through to the
// arguments the daemon is called with, under the schema's own key.
func TestCheckRunFiles_IsRepeatableAndReachesTheRun(t *testing.T) {
	// CONTROL: with no --files the key is ABSENT, not an empty list. An empty
	// list is refused by the analyzer, so setting the key here would turn every
	// unscoped CLI run into a refusal.
	plain, err := parseCheckRunFlags([]string{"--repo", "r", "--language", "go"})
	require.NoError(t, err)
	_, present := checkRunToolArgs(plain, checkRunTestRepo)["files"]
	assert.False(t, present, "an omitted --files must leave the key absent — the caller asked for the whole tree")

	f, err := parseCheckRunFlags([]string{
		"--repo", "r", "--language", "go",
		"--files", "pkg/a.go", "--files", "pkg/b,c.go",
	})
	require.NoError(t, err)
	args := checkRunToolArgs(f, checkRunTestRepo)

	assert.Equal(t, []string{"pkg/a.go", "pkg/b,c.go"}, args["files"],
		"a repeated flag carries every path VERBATIM — the comma-bearing one is exactly what a separated list would have split in two")
	assert.Equal(t, "run", args["operation"])
	assert.Equal(t, "go", args["language"])
	assert.Equal(t, checkRunTestRepo, args["repo"],
		"the RESOLVED tree travels, not the bare argument: a bare name would be re-resolved against the daemon's own root")
}

// TestCheckRunPathPrefix_ReachesTheRun is the OTHER scope flag, held to the same
// standard as --files.
//
// WHY IT IS ITS OWN ROW. A subtree scope that is parsed and then not forwarded
// widens the caller's scan silently from the subtree they named to the whole
// repository — the largest possible reading of the narrowest thing they asked
// for — and the exit status is a perfectly ordinary verdict either way. Nothing
// else in this package would notice.
func TestCheckRunPathPrefix_ReachesTheRun(t *testing.T) {
	f, err := parseCheckRunFlags([]string{
		"--repo", "r", "--language", "go", "--path-prefix", "cmd/knowledge/internal/tools",
	})
	require.NoError(t, err)
	assert.Equal(t, "cmd/knowledge/internal/tools", checkRunToolArgs(f, checkRunTestRepo)["path_prefix"],
		"the subtree the caller named must travel to the run, or the scan silently widens to the whole tree")

	// CONTROL: with no --path-prefix the key is ABSENT rather than an empty
	// string, so an unscoped run asks for the whole tree instead of asking for a
	// subtree spelled "". The two must be distinguishable at the argument.
	plain, err := parseCheckRunFlags([]string{"--repo", "r", "--language", "go"})
	require.NoError(t, err)
	_, present := checkRunToolArgs(plain, checkRunTestRepo)["path_prefix"]
	assert.False(t, present, "an omitted --path-prefix must leave the key absent")
}

// TestCheckRunCompact_IsTriStateAndLeavesTheDefaultToTheRun: the CLI carries an
// explicit choice and NOTHING when the caller made none, so the render default
// is resolved exactly once, by the analyzer's own resolver, on the machine that
// performs the run. A CLI that spelled the default itself would be a second
// answer that can drift from the first.
func TestCheckRunCompact_IsTriStateAndLeavesTheDefaultToTheRun(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		wantPresent bool
		want        bool
	}{
		{name: "no scope, no flag", args: []string{"--repo", "r", "--language", "go"}},
		{name: "a file list and no flag", args: []string{"--repo", "r", "--language", "go", "--files", "a.go"}},
		{
			name: "a file list and an explicit off", wantPresent: true, want: false,
			args: []string{"--repo", "r", "--language", "go", "--files", "a.go", "--compact=false"},
		},
		{
			name: "no scope and an explicit on", wantPresent: true, want: true,
			args: []string{"--repo", "r", "--language", "go", "--compact"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseCheckRunFlags(tc.args)
			require.NoError(t, err)
			got, present := checkRunToolArgs(f, checkRunTestRepo)["compact"]
			require.Equal(t, tc.wantPresent, present,
				"only an explicitly supplied flag may travel; an omitted one must leave the default to the run")
			if tc.wantPresent {
				assert.Equal(t, tc.want, got)
			}
		})
	}

	// KNOWN-POSITIVE CONTROL ON THE ABSENCE. The default the CLI declines to
	// spell is a real, scope-dependent one — compact for a file list and full
	// otherwise — so "the key is absent" is a decision being deferred rather
	// than a knob that turns out not to matter.
	assert.True(t, corpusscan.CompactRenderDefault(nil, []string{"a.go"}))
	assert.False(t, corpusscan.CompactRenderDefault(nil, nil))
}

// TestReportCheckRun_RelaysTheRunVerbatim: the face prints what the run
// answered, byte for byte, and takes only the verdict token from it.
//
// WHY VERBATIM MATTERS. Both render forms come back through this one path, so a
// face that reformatted, re-wrapped or re-counted anything would make the two
// faces of one classification disagree about a run they both describe — and it
// would do so silently, since the exit status would still be right.
func TestReportCheckRun_RelaysTheRunVerbatim(t *testing.T) {
	verdict := "corpus_scan: FLAGGED  checks_flagged=1 sites_flagged=1 checks_refused=0 llm_only_not_executed=0 test_files_scanned=0 truncated=false\n"
	compactBody := verdict + "warning\tpkg/a.go:7\tchk-1\tno naked defer Close\n"
	fullBody := verdict + "[\n  {\n    \"Title\": \"no naked defer Close at pkg/a.go:7\",\n    \"Summary\": \"the whole prose description of the check\"\n  }\n]\n"

	var compact, full bytes.Buffer
	require.ErrorIs(t, reportCheckRunTo(&compact, compactBody), errCheckFlagged)
	require.ErrorIs(t, reportCheckRunTo(&full, fullBody), errCheckFlagged,
		"the render form must not move the exit status — that is the classification's answer, not the renderer's")

	assert.Equal(t, compactBody, compact.String(), "the body is relayed verbatim")
	assert.Equal(t, fullBody, full.String(), "control: the other form is relayed verbatim too")
	assert.NotContains(t, compact.String(), "whole prose description",
		"the compact form drops the description, and the face does not put it back")

	// The verdict line is identical on both, which is what makes the render form
	// a display choice rather than a second classification.
	assert.Equal(t, strings.SplitN(compact.String(), "\n", 2)[0], strings.SplitN(full.String(), "\n", 2)[0])
}

// TestReportCheckRun_RefusesAnAnswerWithNoVerdictToken is the vacuous-green
// guard on the routed path: an answer this face cannot read is an unknown corpus
// state, and the one exit status it must never take is zero.
func TestReportCheckRun_RefusesAnAnswerWithNoVerdictToken(t *testing.T) {
	for _, body := range []string{
		"",
		"corpus_scan: checks_flagged=0 sites_flagged=0\n",
		"manage_checks run: repo is required\n",
		"corpus_scan: PROBABLY_FINE  checks_flagged=0\n",
	} {
		var buf bytes.Buffer
		err := reportCheckRunTo(&buf, body)
		require.Error(t, err, "an unreadable verdict must not exit 0: %q", body)
		code, _ := subcommandExit(err)
		assert.Equal(t, 1, code, "an unreadable verdict is an ordinary failure, not a verdict code")
		assert.Equal(t, body, buf.String(), "the unreadable answer is still shown to the caller")

		// THE MESSAGE IS PART OF THE REFUSAL, not decoration. Whoever reads this
		// line is holding an answer their gate could not classify, and the two
		// things they need are which line was being read and what would have been
		// readable — so the refusal names the analyzer and enumerates all three
		// admitted tokens. A refusal that said only "unreadable" would send them
		// to the CLI to find out what it wanted.
		msg := err.Error()
		assert.Contains(t, msg, corpusscan.AnalyzerName, "the refusal must name the line it was reading")
		for _, token := range []string{tools.VerdictClean, tools.VerdictFlagged, tools.VerdictInconclusive} {
			assert.Contains(t, msg, token, "the refusal must enumerate the admitted token %s", token)
		}
	}

	// KNOWN-POSITIVE CONTROL through the same instrument: a well-formed line IS
	// read, so the refusals above are the token missing rather than the reader
	// rejecting everything.
	var ok bytes.Buffer
	require.NoError(t, reportCheckRunTo(&ok,
		"corpus_scan: CLEAN  checks_flagged=0 sites_flagged=0 checks_refused=0 llm_only_not_executed=0 test_files_scanned=0 truncated=false\n"))
}

// TestCheckRunFiles_ANewlineBearingPathSurvivesTheJSONCarrier is the row the
// JSON encoding was chosen FOR and had not been proven for.
//
// THE COMMA IS NOT THE HARDEST BYTE A PATH CAN HOLD. A comma-separated carrier
// would split one legitimate path in two, which the comma row above covers; a
// NEWLINE is what a line-oriented carrier would split, and every render, log line
// and shell pipeline between this face and the walk is line-oriented. A
// filesystem accepts it and the walk scans it, so the only thing standing
// between a newline-bearing diff entry and a silently dropped file is that every
// hop from here to the analyzer is JSON. This row drives that claim rather than
// restating it.
func TestCheckRunFiles_ANewlineBearingPathSurvivesTheJSONCarrier(t *testing.T) {
	const weird = "pkg/we\nird.go"

	f, err := parseCheckRunFlags([]string{"--repo", "r", "--language", "go", "--files", weird})
	require.NoError(t, err)
	args := checkRunToolArgs(f, checkRunTestRepo)
	require.Equal(t, []string{weird}, args["files"], "control: the flag carries the path verbatim before any encoding")

	// THE CARRIER, hop for hop, exactly as the routed face builds it.
	encodedArgs, err := json.Marshal(args)
	require.NoError(t, err)
	params, err := json.Marshal(kgtools.CallToolParams{Name: "manage_checks", Arguments: encodedArgs})
	require.NoError(t, err)

	// The newline must be ESCAPED on the wire — a raw one would end the JSON
	// string — and must decode back to the byte the caller wrote.
	assert.NotContains(t, string(params), weird,
		"a raw newline inside a JSON string would not parse; the encoder must escape it")

	var decodedParams kgtools.CallToolParams
	require.NoError(t, json.Unmarshal(params, &decodedParams))
	var decoded struct {
		Files []string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(decodedParams.Arguments, &decoded))
	assert.Equal(t, []string{weird}, decoded.Files,
		"the path arrives at the tool BYTE-IDENTICAL, newline and all — which is the whole reason the scope is carried as JSON")
}
