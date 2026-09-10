// SPDX-License-Identifier: Apache-2.0

// files_graph_scope_test.go — the GRAPH arm under a file-list scope.
//
// IT IS THE MIRROR IMAGE OF THE SILENT DROP THE REST OF THIS SCOPE CLOSES. A
// file-list run leaves PathPrefix empty by construction, and the graph arm's own
// narrowing was keyed on that prefix, so a graph check would have evaluated the
// WHOLE code graph and could flag a site in a file the caller never named.
//
// NO CORPUS CHECK REACHES THIS ARM AT THIS TREE — the go corpus is entirely
// ast_pattern plus the accepted llm_only lane — so these rows are written
// against a constructed check through the package's own graphCheckMeta, which is
// the only venue that exercises the arm at all.

package corpusscan

import (
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// graphScopeCaller seeds the graph corpus plus a code graph whose violating node
// lives in pkg/a.go and whose conforming controls live beside it.
func graphScopeCaller() *fakeCaller {
	return newFakeCaller().
		seed("checks", []*knowledgev1.Node{
			checkNode("chk-graph", "no function calls another", "keep the call graph flat",
				graphCheckMeta(corpus.CheckGraphAssertion, noOutboundCalls, "warning", "fx-graph-bad", "fx-graph-good")),
			exampleNode("fx-graph-bad", callsBad),
			exampleNode("fx-graph-good", callsGood),
		}, nil).
		seed("code/repo", []*knowledgev1.Node{
			codeNode("pkg/a.go:caller"),
			codeNode("pkg/a.go:helper"),
			codeNode("other/b.go:outsider"),
			codeNode("other/b.go:target"),
		}, []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeCalls), FromId: "pkg/a.go:caller", ToId: "pkg/a.go:helper"},
			{Type: string(kgtypes.EdgeCalls), FromId: "other/b.go:outsider", ToId: "other/b.go:target"},
		})
}

// graphScopeRepo is a tree holding the files the rows below name, so the
// pre-walk resolve has something to resolve.
func graphScopeRepo(t *testing.T) string {
	t.Helper()
	return seedRepo(t, map[string]string{
		"pkg/a.go":   knownNegative,
		"other/b.go": knownNegative,
	})
}

// TestCorpusScan_GraphArmIsNarrowedByTheNamedFiles: a graph check under a file
// list evaluates the named files' nodes and no others.
func TestCorpusScan_GraphArmIsNarrowedByTheNamedFiles(t *testing.T) {
	root := graphScopeRepo(t)
	gc := graphScopeCaller()

	// CONTROL: unnarrowed, BOTH violating nodes are flagged. Without it, "the
	// narrowed run flagged one" is also what a broken narrowing that always
	// evaluated a single node would produce.
	whole := matchFindings(runScan(t, scanRequest(gc, "repo", root)))
	if len(whole) != 2 {
		t.Fatalf("control: both calling functions violate the assertion, got %d: %+v", len(whole), whole)
	}

	sites := matchFindings(runScan(t, withFiles(scanRequest(gc, "repo", root), "pkg/a.go")))
	if len(sites) != 1 {
		t.Fatalf("a file list must narrow the graph arm's candidates too, or a scan of one file flags sites in files nobody named, got %d: %+v", len(sites), sites)
	}
	if got := sites[0].Metadata[MetaKeyFile]; got != "pkg/a.go" {
		t.Errorf("the surviving site must be in a named file, got %q", got)
	}
}

// TestCorpusScan_GraphCheckNarrowedAwayIsDisclosedNotRun is the empty-candidate
// control learning the one difference a file scope creates: zero candidates
// under a ten-file diff is the scope working, not a missing graph.
func TestCorpusScan_GraphCheckNarrowedAwayIsDisclosedNotRun(t *testing.T) {
	root := graphScopeRepo(t)
	findings := runScan(t, withFiles(scanRequest(graphScopeCaller(), "repo", root), "other/b.go"))

	// It DID narrow: the site in the named file is the only one reported.
	sites := matchFindings(findings)
	if len(sites) != 1 || sites[0].Metadata[MetaKeyFile] != "other/b.go" {
		t.Fatalf("control: the named file's own violating node must still be flagged, got %+v", sites)
	}

	// Now the case that has no candidates at all: a named file the code graph
	// carries no node for.
	quiet := runScan(t, withFiles(scanRequest(graphScopeCaller(), "repo", root+"/pkg"), "a.go"))
	disclosure := findingsByTitlePrefix(quiet, DisclosurePrefixGraphNotRun)
	if len(disclosure) != 1 {
		t.Fatalf("a check whose candidates the scope narrowed away must be recorded as NOT RUN, got %d such findings in %+v", len(disclosure), quiet)
	}
	if !strings.Contains(disclosure[0].Title, "chk-graph") {
		t.Errorf("the disclosure must name the check that did not run, got %q", disclosure[0].Title)
	}
	if v := ClassifyRun(quiet); v.SitesFlagged != 0 || v.ChecksRefused != 0 {
		t.Errorf("a not-run disclosure is neither a flagged site nor a refusal, got %+v", v)
	}
}

// TestCorpusScan_GraphArmStillErrorsOnAnUncollectedGraph is the discriminating
// control for the row above: the not-run arm must not have swallowed the error
// that tells an operator to run a collect.
func TestCorpusScan_GraphArmStillErrorsOnAnUncollectedGraph(t *testing.T) {
	root := graphScopeRepo(t)
	uncollected := newFakeCaller().seed("checks", []*knowledgev1.Node{
		checkNode("chk-graph", "no function calls another", "",
			graphCheckMeta(corpus.CheckGraphAssertion, noOutboundCalls, "warning", "fx-graph-bad", "fx-graph-good")),
		exampleNode("fx-graph-bad", callsBad),
		exampleNode("fx-graph-good", callsGood),
	}, nil)

	err := runScanErr(withFiles(scanRequest(uncollected, "repo", root), "pkg/a.go"))
	if err == nil {
		t.Fatal("an uncollected code graph must still ERROR under a file scope — zero candidates there is a missing graph, not a narrowing")
	}
	if !strings.Contains(err.Error(), "collect") {
		t.Errorf("the error must still tell the operator a collect is required, got %q", err)
	}
}

// TestCorpusScan_GraphSiteCompactRowCarriesNoLine is row class B, and the
// mutation it exists to catch is the instinct graphSiteFinding's own comment
// forbids by name: filling the absent line key with a zero, or printing the node
// id in the column documented as file:line.
func TestCorpusScan_GraphSiteCompactRowCarriesNoLine(t *testing.T) {
	root := graphScopeRepo(t)
	sites := matchFindings(runScan(t, withFiles(scanRequest(graphScopeCaller(), "repo", root), "pkg/a.go")))
	if len(sites) != 1 {
		t.Fatalf("expected the one narrowed site, got %+v", sites)
	}
	cols := strings.Split(CompactLine(sites[0]), "\t")
	if len(cols) != 4 {
		t.Fatalf("every compact row carries the same four columns, got %d: %q", len(cols), cols)
	}
	if cols[1] != "pkg/a.go" {
		t.Errorf("a graph row's second column is the FILE with no line suffix, got %q", cols[1])
	}
	if strings.Contains(cols[1], ":") {
		t.Errorf("a code-graph node yields a file and never a line, so no :line and no :0 may appear here, got %q", cols[1])
	}
	if cols[3] != "pkg/a.go:caller" {
		t.Errorf("a graph row's fourth column is the WHOLE node id — a truncated id does not resolve — got %q", cols[3])
	}
	if !compactLineIsWithinBound(sites[0]) {
		t.Errorf("a graph row must be at or under the documented %d-byte bound, got %d: %q",
			CompactRowBoundGraph, len(CompactLine(sites[0])), CompactLine(sites[0]))
	}
}
