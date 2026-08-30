// SPDX-License-Identifier: Apache-2.0

package corpusscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// TestCorpusScan_RefusesUnvalidatedCheck seeds a failing check and a PASSING
// SIBLING in the same run.
//
// The passing sibling is the KNOWN-POSITIVE CONTROL: without it the test is
// equally satisfied by a gate that refuses everything, which would pass green
// while silently disabling the scanner.
func TestCorpusScan_RefusesUnvalidatedCheck(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": deferCloseBad})
	gc := astCorpus(
		// Passes: fires on bad, silent on good.
		checkNode("chk-ok", "passes", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")),
		// State (b): this pattern FIRES on the good example too, so the
		// two-direction rule refuses it.
		checkNode("chk-fires-on-good", "fires on good", "", astCheckMeta("func $N($$$_) { $$$_ }", "warning", "fx-bad", "fx-good")),
		// State (c): a check type with no validator anywhere.
		checkNode("chk-flow", "flow model", "", map[string]string{
			corpus.MetaCheckType:   string(corpus.CheckFlowModel),
			corpus.MetaSeverity:    "warning",
			corpus.MetaLanguage:    "go",
			corpus.MetaDSLPattern:  "{}",
			corpus.MetaFixtureBad:  "fx-bad",
			corpus.MetaFixtureGood: "fx-good",
		}),
	)
	findings := runScan(t, scanRequest(gc, "repo", root))

	if len(matchFindings(findings)) == 0 {
		t.Fatal("the passing sibling must still execute — a gate refusing everything would satisfy every other assertion here")
	}
	byCheck := map[string]foundation.Finding{}
	for _, f := range findingsByTitlePrefix(findings, RefusalPrefixUnvalidated) {
		byCheck[f.Metadata[MetaKeyCheckID]] = f
	}
	if len(byCheck) != 2 {
		t.Fatalf("expected exactly the two refusable checks to be refused, got %v", byCheck)
	}
	for id, want := range map[string]string{
		"chk-fires-on-good": classifyValidation,
		"chk-flow":          classifyNoExecutor,
	} {
		f, ok := byCheck[id]
		if !ok {
			t.Errorf("%s must be refused", id)
			continue
		}
		if f.Severity != foundation.SeverityCritical {
			t.Errorf("%s: a refusal is critical, got %q", id, f.Severity)
		}
		if !strings.Contains(f.Title, id) {
			t.Errorf("%s: the refusal must NAME the check, got %q", id, f.Title)
		}
		// THE STATES MUST STAY DISTINGUISHABLE. Classification is by sentinel;
		// the upstream message rides along as payload and is never asserted on.
		if !strings.Contains(f.Summary, want) {
			t.Errorf("%s: expected the %q classification, got %q", id, want, f.Summary)
		}
	}
	if byCheck["chk-fires-on-good"].Summary == byCheck["chk-flow"].Summary {
		t.Error("states (b) and (c) must be distinguishable, not collapsed into one message")
	}

	// STATE (a) — a contract-malformed node — is the fourth state and is a HARD
	// ERROR at decode rather than a per-check refusal: a corpus carrying a node
	// the contract rejects is one an operator must fix before any result from it
	// means anything. Asserted here so all four states are seen to differ.
	bad := astCorpus(checkNode("chk-malformed", "malformed", "", map[string]string{
		corpus.MetaCheckType: "wishful", corpus.MetaSeverity: "warning", corpus.MetaLanguage: "go",
		corpus.MetaDSLPattern: "x", corpus.MetaFixtureBad: "fx-bad", corpus.MetaFixtureGood: "fx-good",
	}))
	if err := runScanErr(scanRequest(bad, "repo", root)); err == nil {
		t.Error("state (a): a contract-malformed check must error the run, distinguishably from states (b), (c) and (d)")
	}
}

func TestCorpusScan_ErrorsWhenEveryCheckRefused(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": deferCloseBad})
	// CONTROL: the same shape with ONE passing check must NOT error, so the error
	// below is about "nothing executed" and not about the run path being broken.
	ok := astCorpus(checkNode("chk-ok", "passes", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))
	if err := runScanErr(scanRequest(ok, "repo", root)); err != nil {
		t.Fatalf("control: a run with one executing check must succeed, got %v", err)
	}

	gc := newFakeCaller().seed("checks", append([]*knowledgev1.Node{
		checkNode("chk-flow", "flow model", "", map[string]string{
			corpus.MetaCheckType:   string(corpus.CheckFlowModel),
			corpus.MetaSeverity:    "warning",
			corpus.MetaLanguage:    "go",
			corpus.MetaDSLPattern:  "{}",
			corpus.MetaFixtureBad:  "fx-bad",
			corpus.MetaFixtureGood: "fx-good",
		}),
		checkNode("llm-1", "needs judgment", "", map[string]string{corpus.MetaLLMOnly: "true", corpus.MetaLanguage: "go"}),
	}, fixtureNodes()...), nil)

	err := runScanErr(scanRequest(gc, "repo", root))
	if err == nil {
		t.Fatal("a run in which nothing executed must ERROR — empty findings would read exactly like a clean scan")
	}
	if !strings.Contains(err.Error(), "none executed") {
		t.Errorf("the error must say nothing executed, got %q", err)
	}
	// Telling an operator "nothing ran" while the corpus held accepted llm_only
	// entries is a true sentence that produces a false belief.
	if !strings.Contains(err.Error(), corpus.MetaLLMOnly) {
		t.Errorf("the whole-run error must name the accepted llm_only count, got %q", err)
	}
}

func TestCorpusScan_ErrorsOnEmptyCorpus(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": deferCloseBad})
	// CONTROL: a corpus holding one check does not take this path.
	ok := astCorpus(checkNode("chk-ok", "passes", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))
	if err := runScanErr(scanRequest(ok, "repo", root)); err != nil {
		t.Fatalf("control: a non-empty corpus must not error, got %v", err)
	}

	empty := newFakeCaller().seed("checks", []*knowledgev1.Node{
		checkNode("prose-1", "guidance", "no machine half", map[string]string{}),
		checkNode("llm-1", "needs judgment", "", map[string]string{corpus.MetaLLMOnly: "true", corpus.MetaLanguage: "go"}),
	}, nil)
	err := runScanErr(scanRequest(empty, "repo", root))
	if err == nil {
		t.Fatal("an empty corpus must ERROR naming the language rather than returning zero findings")
	}
	if !strings.Contains(err.Error(), "language=go") {
		t.Errorf("the error must name the language whose corpus it read, got %q", err)
	}
	if !strings.Contains(err.Error(), corpus.MetaLLMOnly) {
		t.Errorf("the empty-corpus error must name the accepted llm_only count, got %q", err)
	}
}

// TestCorpusScan_MaterializationClassifiedSeparately drives state (d) from BOTH
// of its sources with REAL errors, and never with a fake validator.
func TestCorpusScan_MaterializationClassifiedSeparately(t *testing.T) {
	root := seedRepo(t, map[string]string{"a/a.go": deferCloseBad})
	okCorpus := astCorpus(checkNode("chk-ok", "passes", "", astCheckMeta("defer $X.Close()", "warning", "fx-bad", "fx-good")))

	// KNOWN-POSITIVE CONTROL shared by both subtests: the identical corpus under
	// the default TMPDIR reaches validation and produces non-environment
	// outcomes, so a green below cannot mean "every run refuses".
	t.Run("control_default_tmpdir_executes", func(t *testing.T) {
		findings := runScan(t, scanRequest(okCorpus, "repo", root))
		if len(matchFindings(findings)) == 0 {
			t.Fatal("under a working TMPDIR the check must execute")
		}
		if len(findingsByTitlePrefix(findings, RefusalPrefixEnvironment)) != 0 {
			t.Error("a working environment must produce no environment refusals")
		}
	})

	t.Run("probe", func(t *testing.T) {
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
		if err := probeTempDir(); err == nil {
			t.Fatal("the precondition probe must fail when the temp directory cannot be created")
		}
		err := runScanErr(scanRequest(okCorpus, "repo", root))
		if err == nil {
			t.Fatal("a failed environment probe refuses every check, so Run must return the whole-run error")
		}
	})

	t.Run("probe_refuses_without_calling_the_validator", func(t *testing.T) {
		// The SAME check, admitted twice under a WORKING TMPDIR: once with a
		// probe error injected, once without. A different classification is what
		// proves corpus.ValidateFixtures was never reached in the first case.
		set := corpusSet{Fixtures: map[string]corpus.Fixture{
			"fx-bad":  {ID: "fx-bad", Content: deferCloseBad},
			"fx-good": {ID: "fx-good", Content: deferCloseGood},
		}}
		c := corpus.Check{
			ID: "chk-1", Type: corpus.CheckAstPattern, Severity: foundation.SeverityWarning,
			Language: "go", Pattern: "func $N($$$_) { $$$_ }", FixtureBad: "fx-bad", FixtureGood: "fx-good",
		}
		withProbe, ok := admitCheck(context.Background(), set, c, errors.New("temp directory unavailable"))
		if ok {
			t.Fatal("a failed probe must refuse the check")
		}
		if !strings.HasPrefix(withProbe.Title, RefusalPrefixEnvironment) {
			t.Errorf("a probe failure is state (d), got title %q", withProbe.Title)
		}
		withoutProbe, ok := admitCheck(context.Background(), set, c, nil)
		if ok {
			t.Fatal("control: this check fires on its good example and must be refused by the validator")
		}
		if !strings.Contains(withoutProbe.Summary, classifyValidation) {
			t.Errorf("control: without a probe error the VALIDATOR must run and classify as (b), got %q", withoutProbe.Summary)
		}
	})

	t.Run("sentinel", func(t *testing.T) {
		// A REAL materialization error out of the REAL validator, with no fake
		// validator anywhere: the check compiles fine, but the host cannot place
		// its fixture on disk, so corpus.ValidateFixtures' os.MkdirTemp fails and
		// the error wraps ErrFixtureMaterialization.
		//
		// AN EARLIER DRAFT OF THIS SUBTEST USED treesitter.LangUnknown, on the
		// premise that FixtureFileName's miss would produce the materialization
		// sentinel. MEASURED AGAINST CURRENT CONTRACT SOURCE, IT DOES NOT:
		// ValidateFixtures calls ast.Parse and ast.Compile BEFORE it ever reaches
		// FixtureFileName, and an unknown language fails to compile — so that
		// path returns ErrFixtureValidation, and the case proves the opposite of
		// what it was written to prove. The reference is kept alive below as a
		// negative control so the correction cannot silently rot back.
		c := corpus.Check{
			ID: "chk-1", Type: corpus.CheckAstPattern, Severity: foundation.SeverityWarning,
			Language: "go", Pattern: "defer $X.Close()",
		}
		bad := corpus.Fixture{ID: "fx-bad", Content: deferCloseBad}
		good := corpus.Fixture{ID: "fx-good", Content: deferCloseGood}

		// CONTROL: under a working TMPDIR this exact check VALIDATES, so the
		// failure below is the environment and not the check.
		if err := corpus.ValidateFixtures(context.Background(), c, bad, good); err != nil {
			t.Fatalf("control: this check must validate under a working TMPDIR, got %v", err)
		}

		ro := filepath.Join(t.TempDir(), "read-only")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatalf("seed a read-only temp root: %v", err)
		}
		t.Setenv("TMPDIR", ro)
		err := corpus.ValidateFixtures(context.Background(), c, bad, good)
		if !errors.Is(err, corpus.ErrFixtureMaterialization) {
			t.Fatalf("expected a materialization sentinel, got %v", err)
		}
		f := classifyRefusal(c, err)
		if !strings.HasPrefix(f.Title, RefusalPrefixEnvironment) {
			t.Errorf("the materialization sentinel is state (d), got title %q", f.Title)
		}
		if strings.Contains(f.Summary, classifyValidation) {
			t.Error("state (d) must NOT be classified as state (b)")
		}

		// NEGATIVE CONTROL for the corrected premise: an unknown language fails
		// at COMPILE and is therefore state (b), not state (d).
		unknown := corpus.Check{ID: "chk-unknown", Type: corpus.CheckAstPattern, Language: treesitter.LangUnknown, Pattern: "$X"}
		uerr := corpus.ValidateFixtures(context.Background(), unknown, bad, good)
		if !errors.Is(uerr, corpus.ErrFixtureValidation) {
			t.Errorf("an unknown language fails to COMPILE, so it is state (b); got %v", uerr)
		}
	})
}
