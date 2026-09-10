// SPDX-License-Identifier: Apache-2.0

// check_subcommand_include_tests_test.go — the shell face of the test-file knob.
//
// The CLI is the face a plan criterion uses, because it carries an exit status.
// A knob that reached only the MCP tool would leave the two faces answering
// different questions about the same corpus, which is the divergence the shared
// classifier exists to prevent.

package bootstrap

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
)

// TestParseCheckRunFlags_IncludeTestsIsTriState pins the three input states at
// the flag layer. The pointer is what distinguishes them: an omitted flag is
// legal for every language while an explicit one is refused for a language with
// no test-file convention, and a plain bool cannot tell those apart.
func TestParseCheckRunFlags_IncludeTestsIsTriState(t *testing.T) {
	base := []string{"--repo", "r", "--language", "go"}

	t.Run("omitted leaves the knob unset", func(t *testing.T) {
		f, err := parseCheckRunFlags(base)
		require.NoError(t, err)
		assert.Nil(t, f.includeTests, "an omitted flag must stay distinguishable from an explicit false")
	})

	for _, tc := range []struct {
		arg  string
		want bool
	}{
		{"--include-tests", true},
		{"--include-tests=true", true},
		{"--include-tests=false", false},
	} {
		t.Run("explicit "+tc.arg, func(t *testing.T) {
			f, err := parseCheckRunFlags(append(append([]string{}, base...), tc.arg))
			require.NoError(t, err)
			require.NotNil(t, f.includeTests, "an explicit flag must be recorded as explicit")
			assert.Equal(t, tc.want, *f.includeTests)
		})
	}

	t.Run("ids still arrive positionally beside the flag", func(t *testing.T) {
		f, err := parseCheckRunFlags(append(append([]string{}, base...), "--include-tests", "chk-1", "chk-2"))
		require.NoError(t, err)
		assert.Equal(t, []string{"chk-1", "chk-2"}, f.ids,
			"the flag must not swallow the positional check ids")
	})
}

// TestCheckRunToolArgs_CarriesTheKnob pins the wiring from the flag to the
// arguments the run is asked with, which is the step a flag-parsing test alone
// cannot see. It moved from the analyzer's Extra map to the manage_checks
// arguments when the face stopped running the scan in-process; the property is
// the same one, at the seam the code now crosses.
func TestCheckRunToolArgs_CarriesTheKnob(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		want  bool
		unset bool
	}{
		{name: "omitted", args: []string{"--repo", "r", "--language", "go"}, unset: true},
		{name: "true", args: []string{"--repo", "r", "--language", "go", "--include-tests"}, want: true},
		{name: "false", args: []string{"--repo", "r", "--language", "go", "--include-tests=false"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseCheckRunFlags(tc.args)
			require.NoError(t, err)
			got, present := checkRunToolArgs(f, checkRunTestRepo)["include_tests"]
			if tc.unset {
				assert.False(t, present, "an omitted flag must leave the key absent, not set to a value the caller never wrote")
				return
			}
			require.True(t, present, "an explicit flag must reach the run")
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestReportCheckRun_RelaysTheTestFileCount is R3 at this face, driven over the
// SAME shared findings table the cross-face agreement test uses, so the two
// faces cannot come to disagree about what they are counting.
//
// WHAT IT ASSERTS NOW THE RUN IS ROUTED. The count is the run's own, folded by
// corpusscan.ClassifyRun on the machine that performed the scan; this face's
// duty is to relay the line UNCHANGED. A face that re-rendered the counters
// would be a second answer to a question already answered, and this row is what
// catches it: every row's rendered count must equal what the shared fold makes
// of the same findings.
func TestReportCheckRun_RelaysTheTestFileCount(t *testing.T) {
	sawNonZero := false
	for _, tc := range checkVerdictFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			want := corpusscan.ClassifyRun(tc.findings).TestFilesScanned
			if want > 0 {
				sawNonZero = true
			}
			var buf bytes.Buffer
			_ = reportCheckRunTo(&buf, renderedRunFor(tc.findings))
			line, _, _ := strings.Cut(buf.String(), "\n")
			assert.Contains(t, line, fmt.Sprintf("test_files_scanned=%d", want),
				"the shell face relays the shared fold's count, not one of its own")
		})
	}
	// KNOWN-POSITIVE CONTROL: the table drove at least one non-zero count, so
	// the agreement above is not satisfied by two faces that both always say 0.
	assert.True(t, sawNonZero, "the shared table must drive a run that reached test files")

	// The whole line for the empty run, so a counter silently dropped between
	// the run and this face is caught rather than being invisible to a
	// contains-check.
	var buf bytes.Buffer
	require.NoError(t, reportCheckRunTo(&buf, renderedRunFor(nil)))
	assert.Contains(t, strings.SplitN(buf.String(), "\n", 2)[0],
		"corpus_scan: CLEAN  checks_flagged=0 sites_flagged=0 checks_refused=0 llm_only_not_executed=0 test_files_scanned=0 truncated=false")
}
