// SPDX-License-Identifier: Apache-2.0

// check_scope_test.go — a check that declares a repo or path scope does not run
// on a file outside it, and a check that declares none runs everywhere.
//
// EVERY ROW HERE DRIVES THE ANALYZER END TO END, through the check NODE's own
// metadata, because that is the only channel this narrowing has: the scope is
// corpus data written by a rule's author, never a run parameter. A unit-level
// row could assert the decision function while the run loop ignored it, which is
// the class this ticket's own review named — a test that reaches the asserted
// value by a path the production does not use.
//
// THE ZERO ROWS CARRY THEIR KNOWN POSITIVE IN THE SAME RUN. "The scoped check
// did not fire on libextra/c.go" is worth nothing without "and the identical
// unscoped check does", over the same tree, through the same analyzer, in the
// same test — because a scope bug and a broken fixture produce the same zero.

package corpusscan

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// scopeRepo is the tree every row below scopes into.
//
// libextra IS THE SEGMENT-BOUNDARY CONTROL AND IT IS NOT DECORATION: a scope of
// `lib` implemented with strings.HasPrefix admits libextra/c.go, and every other
// row in this file passes under that implementation. It is the one file whose
// presence or absence in a result tells the two matchers apart.
func scopeRepo(t *testing.T) string {
	t.Helper()
	return seedRepo(t, map[string]string{
		"lib/a.go":        deferCloseBad,
		"lib/nested/b.go": deferCloseBad,
		"libextra/c.go":   deferCloseBad,
		"other/d.go":      deferCloseBad,
		"lib/clean.go":    knownNegative,
	})
}

// scopedCheckNode builds the shared defer-Close check carrying scope metadata.
// The id is a parameter so a corpus can hold a scoped check and an unscoped one
// at once, which is what lets an out-of-scope disclosure be observed on a run
// that still executes something.
func scopedCheckNode(id string, scope map[string]string) *knowledgev1.Node {
	md := astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")
	maps.Copy(md, scope)
	return checkNode(id, "no naked defer Close", "handle the error Close returns", md)
}

// scopeMeta spells the two scope keys through the declared constants, never as
// literals: a test keying on a hand-typed copy of a metadata key is green
// against a rename that breaks the product.
//
// IT ENCODES THE ARRAY WITH THE STANDARD LIBRARY AND NOT WITH THE PRODUCTION
// ENCODER, deliberately. An input built by the same function the decoder is
// paired with round-trips through both sides of one implementation and would
// stay green if the pair agreed on an encoding nothing else in the system reads.
// The contract is "a JSON array of repo-relative paths", so the fixture is a
// JSON array, written by json.Marshal.
func scopeMeta(repo string, paths ...string) map[string]string {
	md := map[string]string{}
	if repo != "" {
		md[kgtypes.MetaKeyStyleScopeRepo] = repo
	}
	if len(paths) > 0 {
		encoded, err := json.Marshal(paths)
		if err != nil {
			panic(err)
		}
		md[kgtypes.MetaKeyStyleScopePaths] = string(encoded)
	}
	return md
}

// rawScopeMeta carries a VERBATIM metadata value, for the malformed encodings a
// well-formed encode can never produce.
func rawScopeMeta(key, raw string) map[string]string {
	return map[string]string{key: raw}
}

// leadTitled returns the lead findings whose title starts with prefix.
func leadTitled(findings []foundation.Finding, prefix string) []foundation.Finding {
	return findingsByTitlePrefix(findings, prefix)
}

// TestCheckScope_RepoScopedCheckDoesNotRunOnAnotherRepo is requirement 3's repo
// leg. The out-of-scope run keeps an UNSCOPED check beside the scoped one, so
// the run still executes something and the disclosure is observable rather than
// swallowed by the whole-run refusal the next test asserts.
func TestCheckScope_RepoScopedCheckDoesNotRunOnAnotherRepo(t *testing.T) {
	root := scopeRepo(t)
	gc := astCorpus(
		scopedCheckNode("chk-scoped", scopeMeta("knowledge")),
		scopedCheckNode("chk-open", nil),
	)

	// CONTROL: scanning the repo the check names, BOTH checks run and each flags
	// all four sites. Without this row a scope that never ran anything would
	// satisfy the assertion below.
	matching := runScan(t, scanRequest(gc, "knowledge", root))
	if got := len(matchFindings(matching)); got != 8 {
		t.Fatalf("control: on repo %q both checks must flag all four sites (8 findings), got %d", "knowledge", got)
	}
	if len(leadTitled(matching, DisclosurePrefixOutOfScope)) != 0 {
		t.Errorf("control: a check scanning the repo it names is not out of scope")
	}

	other := runScan(t, scanRequest(gc, "somewhere-else", root))
	sites := matchFindings(other)
	if got := len(sites); got != 4 {
		t.Fatalf("on another repo only the UNSCOPED check may flag, so four sites, got %d", got)
	}
	for _, f := range sites {
		if f.Metadata[MetaKeyCheckID] != "chk-open" {
			t.Errorf("the repo-scoped check must not flag a site on another repo, got one from %q", f.Metadata[MetaKeyCheckID])
		}
	}

	// THE REPORT SAYS OUT OF SCOPE, NOT CLEAN. The two words are the requirement:
	// a reader must be able to tell "this check did not look here" from "this
	// check looked and found nothing".
	out := leadTitled(other, DisclosurePrefixOutOfScope)
	if len(out) != 1 {
		t.Fatalf("the scoped check must be disclosed BY NAME as out of scope, got %d disclosures", len(out))
	}
	if !strings.Contains(out[0].Title, "chk-scoped") || out[0].Metadata[MetaKeyCheckID] != "chk-scoped" {
		t.Errorf("the disclosure must name the check, got title %q metadata %v", out[0].Title, out[0].Metadata)
	}
	if !strings.Contains(out[0].Summary, kgtypes.MetaKeyStyleScopeRepo+"=knowledge") {
		t.Errorf("the disclosure must state the scope that excluded it, got %q", out[0].Summary)
	}

	// AND IT IS NOT A REFUSAL AND NOT A FLAG. An out-of-scope check is doing what
	// its author declared, so it must leave the verdict's own counters alone.
	v := ClassifyRun(other)
	if v.ChecksRefused != 0 {
		t.Errorf("an out-of-scope check is not a refusal, got checks_refused=%d", v.ChecksRefused)
	}
	if v.SitesFlagged != 4 {
		t.Errorf("the disclosure must not count as a flagged site, got sites_flagged=%d", v.SitesFlagged)
	}
}

// TestCheckScope_EveryCheckOutOfScopeIsRefusedNotClean closes the vacuous green
// the previous test's second corpus cannot reach: when NOTHING ran, a report of
// disclosures with a CLEAN verdict would be a scan that read nothing presented
// as a scan that found nothing.
func TestCheckScope_EveryCheckOutOfScopeIsRefusedNotClean(t *testing.T) {
	root := scopeRepo(t)
	gc := astCorpus(scopedCheckNode("chk-scoped", scopeMeta("knowledge")))

	// CONTROL: the same one-check corpus on the repo it names runs and flags.
	if got := len(matchFindings(runScan(t, scanRequest(gc, "knowledge", root)))); got != 4 {
		t.Fatalf("control: the check must run on the repo it names, got %d sites", got)
	}

	err := runScanErr(scanRequest(gc, "somewhere-else", root))
	if err == nil {
		t.Fatal("a run in which not one check was in scope executed nothing, and a run that executed nothing is not a clean run")
	}
	for _, want := range []string{"somewhere-else", kgtypes.MetaKeyStyleScopeRepo, "out of scope"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q; got %q", want, err)
		}
	}
}

// TestCheckScope_PathScopedCheckRunsOnlyUnderItsPrefixes is requirement 3's path
// leg, with the segment boundary as its own row.
func TestCheckScope_PathScopedCheckRunsOnlyUnderItsPrefixes(t *testing.T) {
	root := scopeRepo(t)

	// CONTROL: unscoped, the same check flags every one of the four files.
	whole := siteFiles(runScan(t, scanRequest(astCorpus(scopedCheckNode("chk-1", nil)), "knowledge", root)))
	if len(whole) != 4 {
		t.Fatalf("control: the unscoped check must flag all four files, got %v", whole)
	}

	got := siteFiles(runScan(t, scanRequest(astCorpus(scopedCheckNode("chk-1", scopeMeta("", "lib"))), "knowledge", root)))
	want := []string{"lib/a.go", "lib/nested/b.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("a check scoped to %q must flag only files under it, want %v got %v", "lib", want, got)
	}
	for _, outside := range []string{"libextra/c.go", "other/d.go"} {
		for _, f := range got {
			if f == outside {
				t.Errorf("%s is outside the scope and must not be flagged", outside)
			}
		}
	}
}

// TestCheckScope_PathScopeMatchesAtSegmentBoundaries is the boundary rule on its
// own, stated as the S1 index's own table so the two sides are asserted to agree
// ROW FOR ROW rather than by a claim that they share a function.
//
// IT IS THE ONE PROPERTY A SECOND MATCHER WOULD BREAK. Every other row in this
// file is green under a strings.HasPrefix implementation.
func TestCheckScope_PathScopeMatchesAtSegmentBoundaries(t *testing.T) {
	root := seedRepo(t, map[string]string{
		"pkg/x.go":      deferCloseBad,
		"pkgextra/x.go": deferCloseBad,
	})
	for _, tc := range []struct {
		name  string
		scope string
		want  []string
	}{
		{"a prefix admits what is under it", "pkg", []string{"pkg/x.go"}},
		{"a prefix never admits its sibling", "pkgextra", []string{"pkgextra/x.go"}},
		{"a whole path is its own scope", "pkg/x.go", []string{"pkg/x.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc := astCorpus(scopedCheckNode("chk-1", scopeMeta("", tc.scope)))
			got := siteFiles(runScan(t, scanRequest(gc, "knowledge", root)))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("scope %q: want %v, got %v — a prefix matches at whole path SEGMENTS", tc.scope, tc.want, got)
			}
		})
	}
}

// TestCheckScope_BothKeysApplyTogether is requirement 3's third row: the two
// legs are AND-ed, and each is observed failing while the other passes, so
// neither can be the only one in force.
func TestCheckScope_BothKeysApplyTogether(t *testing.T) {
	root := scopeRepo(t)
	both := scopeMeta("knowledge", "lib")

	// Both legs pass: the check runs, narrowed.
	inScope := siteFiles(runScan(t, scanRequest(astCorpus(
		scopedCheckNode("chk-1", both), scopedCheckNode("chk-open", nil)), "knowledge", root)))
	if len(inScope) != 6 {
		t.Fatalf("both legs matching: the scoped check flags 2 and the open one 4, want 6 sites, got %v", inScope)
	}

	// The REPO leg fails while the path leg would pass.
	repoFails := runScan(t, scanRequest(astCorpus(
		scopedCheckNode("chk-1", both), scopedCheckNode("chk-open", nil)), "somewhere-else", root))
	if got := len(matchFindings(repoFails)); got != 4 {
		t.Fatalf("the repo leg alone must exclude the check, want the open check's 4 sites, got %d", got)
	}

	// The PATH leg fails while the repo leg passes.
	pathFails := runScan(t, scanRequest(astCorpus(
		scopedCheckNode("chk-1", scopeMeta("knowledge", "nowhere")), scopedCheckNode("chk-open", nil)), "knowledge", root))
	if got := len(matchFindings(pathFails)); got != 4 {
		t.Fatalf("the path leg alone must exclude the check, want the open check's 4 sites, got %d", got)
	}
	if len(leadTitled(pathFails, DisclosurePrefixOutOfScope)) != 1 {
		t.Error("a path scope naming nothing in the tree is disclosed by name, never folded into a clean verdict")
	}
}

// TestCheckScope_UnscopedCorpusRendersExactlyAsBefore is requirement 3's
// no-change leg, and it is a BYTE comparison against a render captured from the
// tree before this narrowing existed rather than a count.
//
// A COUNT WOULD NOT CATCH THE REGRESSION THIS GUARDS. The risk of a per-check
// narrowing is not that it drops a site; it is that it adds a line — a
// disclosure, a scope column, a reordering — to every run of every corpus that
// never asked for it. Only the bytes can say that nothing was added.
func TestCheckScope_UnscopedCorpusRendersExactlyAsBefore(t *testing.T) {
	root := seedRepo(t, map[string]string{
		"lib/a.go":     deferCloseBad,
		"other/d.go":   deferCloseBad,
		"lib/clean.go": knownNegative,
	})
	findings := runScan(t, scanRequest(astCorpus(scopedCheckNode("chk-1", nil)), "knowledge", root))
	got, err := RenderCompact(findings)
	if err != nil {
		t.Fatalf("RenderCompact: %v", err)
	}
	if got != unscopedRenderGolden {
		t.Errorf("an unscoped corpus must render BYTE-IDENTICALLY to the base tree.\n--- want ---\n%s\n--- got ---\n%s", unscopedRenderGolden, got)
	}
	if v := ClassifyRun(findings); v.ChecksRefused != 0 || v.SitesFlagged != 2 {
		t.Errorf("an unscoped corpus's verdict is untouched, got %+v", v)
	}
}

// unscopedRenderGolden is the compact render an unscoped corpus produced on the
// tree BEFORE the per-check narrowing landed, CAPTURED by running this test's
// own inputs through the analyzer at that tree with the narrowing's production
// files removed, and pasted verbatim. The leading "[]" is RenderFindings over an
// empty lead block, which is what "this run disclosed nothing" looks like.
const unscopedRenderGolden = "[]\n" +
	"warning\tlib/a.go:8\tchk-1\tno naked defer Close\n" +
	"warning\tother/d.go:8\tchk-1\tno naked defer Close\n"

// TestCheckScope_MalformedScopeRefusesTheCheckByName is the bad-input leg. A
// scope the scan cannot read must never be resolved to "everywhere", which is
// the widest possible reading of a value the corpus got wrong.
func TestCheckScope_MalformedScopeRefusesTheCheckByName(t *testing.T) {
	root := scopeRepo(t)
	for _, tc := range []struct {
		name string
		meta map[string]string
		want string
	}{
		{"paths that are not a JSON array", rawScopeMeta(kgtypes.MetaKeyStyleScopePaths, "lib"), "JSON array"},
		{"an empty path array", rawScopeMeta(kgtypes.MetaKeyStyleScopePaths, "[]"), "EMPTY array"},
		{"an empty path element", rawScopeMeta(kgtypes.MetaKeyStyleScopePaths, `["lib",""]`), "is empty"},
		{"an absolute path", rawScopeMeta(kgtypes.MetaKeyStyleScopePaths, `["/lib"]`), "absolute"},
		{"an escaping path", rawScopeMeta(kgtypes.MetaKeyStyleScopePaths, `["../lib"]`), ".."},
		{"an uncanonical path", rawScopeMeta(kgtypes.MetaKeyStyleScopePaths, `["./lib"]`), "canonicalize"},
		{"a repo scope that is a path", rawScopeMeta(kgtypes.MetaKeyStyleScopeRepo, "org/knowledge"), "path separator"},
		{"an empty repo scope", rawScopeMeta(kgtypes.MetaKeyStyleScopeRepo, ""), "is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The corpus carries an UNSCOPED check beside the malformed one, so the
			// run executes and the refusal is observable in findings rather than as
			// a whole-run error — and so the KNOWN POSITIVE is in the same run.
			gc := astCorpus(scopedCheckNode("chk-bad", tc.meta), scopedCheckNode("chk-open", nil))
			findings := runScan(t, scanRequest(gc, "knowledge", root))

			refusals := leadTitled(findings, RefusalPrefixUnvalidated)
			if len(refusals) != 1 {
				t.Fatalf("a malformed scope must refuse the check BY NAME, got %d refusals", len(refusals))
			}
			if !strings.Contains(refusals[0].Title, "chk-bad") {
				t.Errorf("the refusal must name the check, got %q", refusals[0].Title)
			}
			if !strings.Contains(refusals[0].Summary, tc.want) {
				t.Errorf("the refusal must name what is wrong with the value (%q), got %q", tc.want, refusals[0].Summary)
			}

			// checks_refused counts it, so the run reads INCONCLUSIVE and never CLEAN.
			v := ClassifyRun(findings)
			if v.ChecksRefused != 1 {
				t.Errorf("a malformed scope increments checks_refused, got %d", v.ChecksRefused)
			}
			if !v.Inconclusive() || v.Clean() {
				t.Errorf("a run carrying a refusal is INCONCLUSIVE and never clean, got %+v", v)
			}

			// AND THE CHECK DID NOT RUN. The known positive is in the tree and the
			// UNSCOPED twin fires on all four files, so a refused check contributing
			// zero of them is the refusal working rather than an empty corpus.
			for _, f := range matchFindings(findings) {
				if f.Metadata[MetaKeyCheckID] == "chk-bad" {
					t.Errorf("a refused check must not walk: got a site from it at %v", f.Metadata[MetaKeyFile])
				}
			}
			if got := len(matchFindings(findings)); got != 4 {
				t.Errorf("known positive: the unscoped twin must still flag all four sites, got %d", got)
			}
		})
	}
}

// TestCheckScope_ScopeAppliedLineRidesTheCompactRender is the render leg: the
// per-check scope line is a LEAD finding, so it renders in full above the
// compact block and is never compacted into a site row.
func TestCheckScope_ScopeAppliedLineRidesTheCompactRender(t *testing.T) {
	root := scopeRepo(t)
	findings := runScan(t, scanRequest(astCorpus(scopedCheckNode("chk-1", scopeMeta("", "lib"))), "knowledge", root))

	applied := leadTitled(findings, DisclosurePrefixScopeApplied)
	if len(applied) != 1 {
		t.Fatalf("a check that RAN under its own narrowing must say so once, got %d lines", len(applied))
	}
	if !IsLeadFinding(applied[0]) {
		t.Error("the scope line is a lead finding: a one-line site row cannot carry which paths a check answered for")
	}
	if !strings.Contains(applied[0].Summary, "lib") {
		t.Errorf("the line must state the scope applied, got %q", applied[0].Summary)
	}

	rendered, err := RenderCompact(findings)
	if err != nil {
		t.Fatalf("RenderCompact: %v", err)
	}
	if !strings.Contains(rendered, applied[0].Title) {
		t.Error("the compact render must carry the scope line above its block")
	}
	if strings.Contains(rendered, CompactLine(applied[0])) && strings.Contains(CompactLine(applied[0]), "\t") {
		t.Error("the scope line must not be compacted into a tab-separated site row")
	}
	// AND IT IS NOT A SITE. Without an arm in the fold, every scoped run would
	// read FLAGGED with a non-zero exit.
	if got := ClassifyRun(findings).SitesFlagged; got != 2 {
		t.Errorf("the scope line must not count as a flagged site, got sites_flagged=%d", got)
	}
}

// TestCheckScope_UnscopedCheckRunsEverywhere is the default leg, stated on its
// own because it is the majority of every corpus: the narrowing must be
// something a check opts into.
func TestCheckScope_UnscopedCheckRunsEverywhere(t *testing.T) {
	root := scopeRepo(t)
	findings := runScan(t, scanRequest(astCorpus(scopedCheckNode("chk-1", nil)), "knowledge", root))
	if got := len(siteFiles(findings)); got != 4 {
		t.Fatalf("a check with no scope runs everywhere, got %d sites", got)
	}
	for _, prefix := range []string{DisclosurePrefixOutOfScope, DisclosurePrefixScopeApplied} {
		if len(leadTitled(findings, prefix)) != 0 {
			t.Errorf("an unscoped corpus emits no %q line", prefix)
		}
	}
}
