// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_run_test.go pins the run operation's two selector properties —
// the id subset actually narrows and an unresolvable id is loud, and the walk
// runs over the NAMED repo — plus the verdict line's two vacuous-green guards.

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// runChecksArgs builds a run payload against a registered repo.
func runChecksArgs(t *testing.T, repo string, extra map[string]any) json.RawMessage {
	t.Helper()
	args := map[string]any{
		"operation": OpChecksRun,
		"repo":      repo,
		"language":  "go",
	}
	maps.Copy(args, extra)
	body, err := json.Marshal(args)
	require.NoError(t, err)
	return body
}

// checksRunFixtureTree writes a tiny Go tree the seeded checks have real sites
// in, and returns its root.
//
// A REAL TREE RATHER THAN THE REPO ITSELF: the site count has to be a known
// quantity for the narrowing assertion to mean anything, and a tree this test
// writes is the only way to know it.
func checksRunFixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/runfixture\n\ngo 1.22\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sites.go"), []byte(
		"package runfixture\n\nfunc alpha() { fmt.Println(1) }\n\nfunc beta() { log.Println(2) }\n"), 0o600))
	return root
}

// The two seeded checks. One matches fmt.Println, the other log.Println, so the
// fixture tree above holds exactly one site for each — which is what lets the
// subset assertion distinguish "narrowed" from "ran everything".
func checksRunCorpus() []*knowledgev1.Node {
	mk := func(id, name, pattern, badBody, goodBody string) []*knowledgev1.Node {
		badID, goodID := id+"-bad", id+"-good"
		return []*knowledgev1.Node{
			{
				Id: id, Type: string(kgtypes.NodeFinding), SymbolName: name,
				Description: "seeded run-fixture check",
				Metadata: map[string]string{
					corpus.MetaCheckType:   string(corpus.CheckAstPattern),
					corpus.MetaSeverity:    string(foundation.SeverityWarning),
					corpus.MetaLanguage:    "go",
					corpus.MetaDSLPattern:  pattern,
					corpus.MetaFixtureBad:  badID,
					corpus.MetaFixtureGood: goodID,
				},
			},
			{
				Id: badID, Type: string(kgtypes.NodeExample), Content: badBody,
				Metadata: map[string]string{corpus.MetaLanguage: "go"},
			},
			{
				Id: goodID, Type: string(kgtypes.NodeExample), Content: goodBody,
				Metadata: map[string]string{corpus.MetaLanguage: "go"},
			},
		}
	}
	nodes := mk("go:fmt-println", "no-fmt-println", "fmt.Println($X)",
		"package p\n\nfunc f() { fmt.Println(1) }\n",
		"package p\n\nfunc f() { log.Println(1) }\n")
	return append(nodes, mk("go:log-println", "no-log-println", "log.Println($X)",
		"package p\n\nfunc f() { log.Println(1) }\n",
		"package p\n\nfunc f() { fmt.Println(1) }\n")...)
}

// driveChecksRun runs the operation against a seeded corpus and a registered repo.
func driveChecksRun(t *testing.T, args json.RawMessage) kgtools.ToolResult {
	t.Helper()
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: newChecksGraphFake(checksRunCorpus()...)}
	handled, res := InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage_checks", Arguments: args})
	require.True(t, handled)
	return res
}

// TestManageChecks_RunNarrowsToTheNamedIDs asserts BOTH arms of the id subset:
// naming ids actually narrows what executes, and an id matching no check is an
// error naming it rather than a silent widening to the whole corpus.
//
// THE SILENT WIDENING IS THE DANGEROUS HALF. A typo'd id that quietly scanned
// everything would still return findings, so the run would look like it worked
// while answering a question nobody asked.
func TestManageChecks_RunNarrowsToTheNamedIDs(t *testing.T) {
	root := checksRunFixtureTree(t)
	m := withTestManifest(t)
	require.NoError(t, m.Record("runfixture", root))

	// CONTROL: the whole corpus flags BOTH sites. Without it, "the narrowed run
	// flagged one" says nothing — one is also what a broken narrowing that always
	// ran a single check would produce.
	whole := driveChecksRun(t, runChecksArgs(t, "runfixture", nil))
	require.False(t, whole.IsError, "the unnarrowed run must succeed: %s", whole.Content[0].Text)
	assert.Contains(t, whole.Content[0].Text, "sites_flagged=2",
		"the fixture tree holds one site for each of the two seeded checks")

	// NARROWED: only the named check runs, so only its site is flagged.
	narrowed := driveChecksRun(t, runChecksArgs(t, "runfixture", map[string]any{
		"ids": []string{"go:fmt-println"},
	}))
	require.False(t, narrowed.IsError, "the narrowed run must succeed: %s", narrowed.Content[0].Text)
	body := narrowed.Content[0].Text
	assert.Contains(t, body, "sites_flagged=1", "naming one check must execute only that check")
	assert.Contains(t, body, "no-fmt-println", "the named check's own site must be reported")
	assert.NotContains(t, body, "no-log-println",
		"a check that was not named must not run — a subset that resolves every id and then scans everything defeats the param")

	// UNRESOLVABLE: loud, naming the id.
	missing := driveChecksRun(t, runChecksArgs(t, "runfixture", map[string]any{
		"ids": []string{"go:fmt-println", "go:no-such-check"},
	}))
	require.True(t, missing.IsError, "an id matching no check must be an error, never a silent widening")
	assert.Contains(t, missing.Content[0].Text, "go:no-such-check",
		"the error must name the id the caller got wrong")
}

// TestManageChecks_RunWalksTheNamedRepoNotTheDaemonRoot asserts the walk-root fix
// at THIS entry point.
//
// run is a SECOND caller into the same analyzer and could regress independently
// of the topology dispatcher, which is why this is asserted here rather than
// inferred from the dispatcher's own test. The deps root is deliberately a tree
// with no sites in it, so a run that took the daemon root would report a clean
// scan of the wrong tree — the defect's silent signature.
func TestManageChecks_RunWalksTheNamedRepoNotTheDaemonRoot(t *testing.T) {
	root := checksRunFixtureTree(t)
	m := withTestManifest(t)
	require.NoError(t, m.Record("runfixture", root))

	// The daemon root is an EMPTY tree: if the walk root were taken from it, the
	// scan would find nothing and report CLEAN.
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: newChecksGraphFake(checksRunCorpus()...)}
	handled, res := InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage_checks", Arguments: runChecksArgs(t, "runfixture", nil)})
	require.True(t, handled)
	require.False(t, res.IsError, "the run must succeed: %s", res.Content[0].Text)
	body := res.Content[0].Text

	assert.Contains(t, body, "sites_flagged=2",
		"the walk must run over the tree the repo argument names, not the daemon --root")
	assert.NotContains(t, body, VerdictClean,
		"a clean verdict here would be a clean scan of the wrong tree, which is the defect's silent signature")

	// KNOWN POSITIVE for the direction of the assertion: the SAME call against a
	// repo whose tree genuinely holds no sites DOES report clean, so the flagged
	// result above is a property of the tree rather than of the tool.
	empty := t.TempDir()
	require.NoError(t, m.Record("emptyfixture", empty))
	handled, clean := InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage_checks", Arguments: runChecksArgs(t, "emptyfixture", nil)})
	require.True(t, handled)
	require.False(t, clean.IsError, "a scan of an empty tree is a clean answer, not an error: %s", clean.Content[0].Text)
	assert.Contains(t, clean.Content[0].Text, VerdictClean)
	assert.Contains(t, clean.Content[0].Text, "sites_flagged=0")
}

// verdictFinding builds one finding with the given title, for the classifier
// tests. The title is what ClassifyRun keys on, so it is the only field that has
// to be real.
func verdictFinding(title string) foundation.Finding {
	return foundation.Finding{Algorithm: corpusscan.AnalyzerName, Title: title}
}

// TestManageChecks_RunVerdictCountsFiresAndRefusals asserts the classifier reads
// the analyzer's own locked title constants.
//
// THE REFUSAL ARM IS THE FALSIFYING ONE. A verdict derived from severity counts
// passes the counting assertion and fails here: a refusal is emitted at critical
// severity, and so is a critical check's genuine site, so severity cannot tell
// them apart — and a run that could not execute half its corpus reporting CLEAN
// is exactly the vacuous green the fixture gate exists to prevent.
func TestManageChecks_RunVerdictCountsFiresAndRefusals(t *testing.T) {
	t.Run("sites and refusals are counted separately", func(t *testing.T) {
		v := corpusscan.ClassifyRun([]foundation.Finding{
			verdictFinding("no-fmt-println at a.go:1"),
			verdictFinding("no-fmt-println at b.go:2"),
			verdictFinding(corpusscan.RefusalPrefixUnvalidated + "go:broken-check"),
		})
		assert.Equal(t, 2, v.SitesFlagged, "two findings flag real sites")
		assert.Equal(t, 1, v.ChecksRefused, "one finding is a refusal, not a site")
		assert.False(t, v.Clean(), "a run carrying a refusal has not scanned its whole corpus")
		assert.True(t, v.Inconclusive())
	})

	t.Run("a refusal ALONE is not clean", func(t *testing.T) {
		// The falsifying case in isolation: nothing was flagged, so every
		// site-count assertion is satisfied, and the run STILL must not read clean.
		v := corpusscan.ClassifyRun([]foundation.Finding{
			verdictFinding(corpusscan.RefusalPrefixEnvironment + "go:some-check"),
		})
		assert.Equal(t, 0, v.SitesFlagged)
		assert.Equal(t, 1, v.ChecksRefused, "the environment refusal prefix must classify as a refusal")
		assert.False(t, v.Clean(), "a scan that could not run a check must never report CLEAN")
		assert.Equal(t, VerdictInconclusive, RunVerdictToken(v))
	})

	t.Run("the llm_only disclosure is neither a site nor a refusal", func(t *testing.T) {
		v := corpusscan.ClassifyRun([]foundation.Finding{{
			Algorithm: corpusscan.AnalyzerName,
			Title:     corpusscan.DisclosureTitleLLMOnly,
			Metrics:   map[string]float64{"llm_only_total": 3},
		}})
		assert.Equal(t, 0, v.SitesFlagged, "the disclosure describes a lane, it does not flag a site")
		assert.Equal(t, 0, v.ChecksRefused)
		assert.Equal(t, 3, v.LLMOnlyNotExecuted)
		assert.True(t, v.Clean(), "an executed corpus with an llm_only lane and no sites IS clean")
	})

	// KNOWN POSITIVE: a genuinely clean run reports CLEAN, so the falses above are
	// a classification rather than a predicate that always denies.
	assert.Equal(t, VerdictClean, RunVerdictToken(corpusscan.ClassifyRun(nil)))
}

// TestManageChecks_RunVerdictIsNotCleanWhenTruncated is separate from the refusal
// arm deliberately: truncation and refusal are produced by DIFFERENT constants,
// and an implementation that handled one and missed the other would pass the
// refusal test while reporting a clipped run as complete.
func TestManageChecks_RunVerdictIsNotCleanWhenTruncated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
	}{
		{"the per-check ceiling", corpusscan.TruncationPrefixCheck + "go:noisy-check"},
		{"the run ceiling", corpusscan.TruncationTitleRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := corpusscan.ClassifyRun([]foundation.Finding{verdictFinding(tc.title)})
			assert.True(t, v.Truncated, "the ceiling notice must set the truncation flag")
			assert.Equal(t, 0, v.SitesFlagged, "a truncation notice is not a flagged site")
			assert.Equal(t, 0, v.ChecksRefused, "a truncation is not a refusal — they are different states")
			assert.False(t, v.Clean(), "a run whose output was clipped has not reported everything it found")
			assert.Equal(t, VerdictInconclusive, RunVerdictToken(v))
		})
	}

	// The rendered line carries the flag too, so a consumer reading the line
	// rather than the struct sees the same answer.
	line := renderRunVerdict(corpusscan.ClassifyRun([]foundation.Finding{
		verdictFinding(corpusscan.TruncationTitleRun),
	}), 1, 1)
	assert.Contains(t, line, "truncated=true")
	assert.Contains(t, line, VerdictInconclusive)
	assert.NotContains(t, line, VerdictClean)

	// KNOWN POSITIVE on the same renderer: an untruncated run renders the flag
	// false and the clean token, so the assertions above are about this input.
	clean := renderRunVerdict(corpusscan.ClassifyRun(nil), 0, 0)
	assert.Contains(t, clean, "truncated=false")
	assert.Contains(t, clean, VerdictClean)
}

// TestManageChecks_RunResolvesTheAnalyzerThroughTheExportedConstant guards the
// one-character trap.
//
// The REGISTERED identifier carries an underscore while the Go PACKAGE does not,
// and a criterion on another ticket spelled it the package's way, was refused by
// the registry outright, and was unrunnable as written. This asserts the name the
// tool resolves is the registry's, by looking it up the same way.
func TestManageChecks_RunResolvesTheAnalyzerThroughTheExportedConstant(t *testing.T) {
	require.Equal(t, "corpus_scan", corpusscan.AnalyzerName,
		"the registered identifier carries an underscore; the Go package name does not")
	_, ok := foundation.Get(corpusscan.AnalyzerName)
	require.True(t, ok, "the constant must resolve in the registry, or run refuses every call")

	// The negative control: the package spelling is NOT registered, which is what
	// makes resolving through the constant load-bearing rather than cosmetic.
	_, wrong := foundation.Get(strings.ReplaceAll(corpusscan.AnalyzerName, "_", ""))
	assert.Falsef(t, wrong, "%q must not resolve — that spelling is the trap", "corpusscan")
}
