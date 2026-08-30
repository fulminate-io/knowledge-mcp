// SPDX-License-Identifier: Apache-2.0

package corpus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// receiverWhere narrows the pattern's receiver capture to the identifier "db".
// It is what makes the good fixture below a CALIBRATED example rather than an
// irrelevant one: the good fixture carries the same defer-Close shape on a
// different receiver, so it is silent because of the narrowing.
const receiverWhere = `{"matches":{"of":"X","regex":"^db$"}}`

// The two fixture bodies differ in the receiver name ONLY, so no assertion in
// this file can be satisfied by the fixtures merely being different text.
const (
	closerPreamble = "package p\n\ntype c struct{}\n\nfunc (c) Close() error { return nil }\n\n"
	badBody        = closerPreamble + "func f() {\n\tdb := c{}\n\tdefer db.Close()\n}\n"
	goodBody       = closerPreamble + "func g() {\n\trows := c{}\n\tdefer rows.Close()\n}\n"
)

// astCheck builds a Check VALUE directly rather than through ParseCheck,
// because ValidateFixtures is exported and a caller may legitimately do the
// same — the error taxonomy has to hold on that route too.
func astCheck(where string) Check {
	c := Check{
		ID:          "check-node",
		Type:        CheckAstPattern,
		Severity:    foundation.SeverityWarning,
		Language:    treesitter.LangGo,
		Pattern:     "defer $X.Close()",
		FixtureBad:  "bad-node",
		FixtureGood: "good-node",
	}
	if where != "" {
		c.Where = []byte(where)
	}
	return c
}

func badFixture(content string) Fixture  { return Fixture{ID: "bad-node", Content: content} }
func goodFixture(content string) Fixture { return Fixture{ID: "good-node", Content: content} }

func TestValidateFixtures_FiresOnBadSilentOnGood(t *testing.T) {
	err := ValidateFixtures(context.Background(), astCheck(receiverWhere),
		badFixture(badBody), goodFixture(goodBody))
	if err != nil {
		t.Fatalf("a calibrated check must be admitted: %v", err)
	}

	// Same pair with no where-tree: the good body's defer-Close now matches the
	// bare shape, so the gate must refuse. This is the control proving the pass
	// above came from the narrowing rather than from the gate admitting anything.
	err = ValidateFixtures(context.Background(), astCheck(""),
		badFixture(badBody), goodFixture(goodBody))
	if err == nil {
		t.Fatal("with the where-tree dropped the good fixture matches the bare shape, so admission is wrong")
	}
}

func TestValidateFixtures_RefusesWhenSilentOnBad(t *testing.T) {
	// The bad fixture carries the shape on the WRONG receiver, so the narrowed
	// check never fires on it.
	err := ValidateFixtures(context.Background(), astCheck(receiverWhere),
		badFixture(goodBody), goodFixture(goodBody))
	if err == nil {
		t.Fatal("a check silent on its bad example must be refused")
	}
	if !errors.Is(err, ErrFixtureValidation) {
		t.Errorf("want ErrFixtureValidation, got %v", err)
	}
	for _, want := range []string{"SILENT", "bad-node"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err.Error(), want)
		}
	}
}

func TestValidateFixtures_RefusesWhenFiresOnGood(t *testing.T) {
	err := ValidateFixtures(context.Background(), astCheck(receiverWhere),
		badFixture(badBody), goodFixture(badBody))
	if err == nil {
		t.Fatal("a check firing on its good example must be refused")
	}
	if !errors.Is(err, ErrFixtureValidation) {
		t.Errorf("want ErrFixtureValidation, got %v", err)
	}
	for _, want := range []string{"FIRES", "good-node"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err.Error(), want)
		}
	}
}

func TestValidateFixtures_RefusesUnexecutableCheckType(t *testing.T) {
	for _, ct := range []CheckType{CheckGraphAssertion, CheckTopologyThreshold, CheckFlowModel} {
		t.Run(string(ct), func(t *testing.T) {
			c := astCheck(receiverWhere)
			c.Type = ct
			err := ValidateFixtures(context.Background(), c, badFixture(badBody), goodFixture(goodBody))
			if err == nil {
				t.Fatalf("%s has no validator here and must be refused", ct)
			}
			if !errors.Is(err, ErrNoExecutor) {
				t.Errorf("want ErrNoExecutor, got %v", err)
			}
			if !strings.Contains(err.Error(), string(ct)) {
				t.Errorf("refusal %q does not name the check type", err.Error())
			}
		})
	}
}

func TestValidateFixtures_RefusesDegradedFixtureParse(t *testing.T) {
	degraded := closerPreamble + "func f() {\n\tdb := c{}\n\tdefer db.Close(\n"
	err := ValidateFixtures(context.Background(), astCheck(receiverWhere),
		badFixture(degraded), goodFixture(goodBody))
	if err == nil {
		t.Fatal("a fixture that only parses under error recovery must be refused")
	}
	if !errors.Is(err, ErrFixtureValidation) {
		t.Errorf("want ErrFixtureValidation, got %v", err)
	}
	if !strings.Contains(err.Error(), "bad-node") {
		t.Errorf("refusal %q does not name the offending fixture", err.Error())
	}
}

// TestValidateFixtures_LiftsOversizeFixture is the BEHAVIORAL catcher for
// ast.Scope.LiftExclusions: an oversize fixture must still be walked.
//
// The known-negative control is the point. "The check still fires" would pass
// just as well if the size rule had never existed, so the same bytes are first
// walked WITHOUT lifting and asserted to be excluded — only then does the
// admission below prove that lifting is what carried it.
func TestValidateFixtures_LiftsOversizeFixture(t *testing.T) {
	oversize := badBody + "\n// " + strings.Repeat("x", 520*1024) + "\n"

	dir := t.TempDir()
	name, ok := treesitter.FixtureFileName(treesitter.LangGo)
	if !ok {
		t.Fatal("go must resolve a fixture filename")
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(oversize), 0o600); err != nil {
		t.Fatal(err)
	}
	pat, err := ast.Parse("defer $X.Close()")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := ast.Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	_, stats, err := ast.Count(context.Background(), dir, treesitter.LangGo, cp, nil, ast.Scope{IncludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesScanned != 0 {
		t.Fatalf("control failed: the oversize fixture was scanned without lifting (files_scanned=%d), so this test cannot show that lifting is what admits it", stats.FilesScanned)
	}

	if err := ValidateFixtures(context.Background(), astCheck(receiverWhere),
		badFixture(oversize), goodFixture(goodBody)); err != nil {
		t.Fatalf("an oversize fixture must still be walked: %v", err)
	}
}

// TestValidateFixtures_RefusesUncalibratedGoodFixture pins the fourth control.
// The good fixture here is silent for the WRONG reason — it has nothing to do
// with the check at all — and the node-id inequality the contract already
// requires cannot see that.
func TestValidateFixtures_RefusesUncalibratedGoodFixture(t *testing.T) {
	err := ValidateFixtures(context.Background(), astCheck(receiverWhere),
		badFixture(badBody), goodFixture("package p\n"))
	if err == nil {
		t.Fatal("a good fixture outside the check's shape population calibrates nothing and must be refused")
	}
	if !errors.Is(err, ErrFixtureValidation) {
		t.Errorf("want ErrFixtureValidation, got %v", err)
	}
	if !strings.Contains(err.Error(), "good-node") {
		t.Errorf("refusal %q does not name the offending fixture", err.Error())
	}
}

// TestValidateFixtures_ErrorsAreClassifiable drives one case per error class and
// asserts errors.Is on each, so a consumer telling an environment fault from a
// bad check never has to read the wording.
//
// No t.Parallel here: the materialization case uses t.Setenv.
func TestValidateFixtures_ErrorsAreClassifiable(t *testing.T) {
	t.Run("materialization", func(t *testing.T) {
		// A VALID check, so compile and where-parse both succeed and creating
		// the temp directory is the first thing that can fail.
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent", "xyz"))
		err := ValidateFixtures(context.Background(), astCheck(receiverWhere),
			badFixture(badBody), goodFixture(goodBody))
		if err == nil {
			t.Fatal("an unusable TMPDIR must fail")
		}
		if !errors.Is(err, ErrFixtureMaterialization) {
			t.Errorf("want ErrFixtureMaterialization, got %v", err)
		}
		if errors.Is(err, ErrFixtureValidation) || errors.Is(err, ErrNoExecutor) {
			t.Errorf("an environment fault must not also classify as a check fault: %v", err)
		}
	})

	t.Run("validation", func(t *testing.T) {
		err := ValidateFixtures(context.Background(), astCheck(receiverWhere),
			badFixture(goodBody), goodFixture(goodBody))
		if !errors.Is(err, ErrFixtureValidation) {
			t.Fatalf("want ErrFixtureValidation, got %v", err)
		}
		if errors.Is(err, ErrFixtureMaterialization) || errors.Is(err, ErrNoExecutor) {
			t.Errorf("a check fault must not also classify as an environment fault: %v", err)
		}
	})

	t.Run("no executor", func(t *testing.T) {
		c := astCheck(receiverWhere)
		c.Type = CheckFlowModel
		err := ValidateFixtures(context.Background(), c, badFixture(badBody), goodFixture(goodBody))
		if !errors.Is(err, ErrNoExecutor) {
			t.Fatalf("want ErrNoExecutor, got %v", err)
		}
		if errors.Is(err, ErrFixtureMaterialization) || errors.Is(err, ErrFixtureValidation) {
			t.Errorf("an unexecutable check type must not also classify as either fixture fault: %v", err)
		}
	})

	// An uncompilable body reaches this function whenever a caller skipped
	// ParseCheck, so it too must classify rather than escape unclassified.
	t.Run("uncompilable body", func(t *testing.T) {
		c := astCheck("")
		c.Pattern = "func $N( {"
		err := ValidateFixtures(context.Background(), c, badFixture(badBody), goodFixture(goodBody))
		if !errors.Is(err, ErrFixtureValidation) {
			t.Fatalf("want ErrFixtureValidation, got %v", err)
		}
	})
}
