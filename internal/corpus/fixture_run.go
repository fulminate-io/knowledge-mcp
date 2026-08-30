// SPDX-License-Identifier: Apache-2.0

// fixture_run.go — the mandatory fixture gate: a check is admitted only once it
// has been shown to FIRE on its bad example and stay SILENT on its good one.
//
// The gate delegates all matching to the ast engine and adds only four things:
// materialization of a fixture's text into a temp directory the walk can read,
// the two-direction rule, the calibration probe, and an error taxonomy that
// tells an environment fault apart from a bad check.
//
// WHY SILENCE ALONE IS NOT EVIDENCE. A detector authored from one incident's
// text matches that incident and nothing else: a call shape occurs on safe and
// unsafe arguments alike, so a good example that simply has nothing to do with
// the check is silent for a reason that says nothing about the check. The
// calibration probe is the mechanical form of that lesson — for a check that
// narrows with a where-tree, the good example is re-run with the where DROPPED
// and must FIRE, proving it sits inside the check's shape population and is
// excluded by the narrowing rather than by irrelevance.
//
// THE HONEST LIMIT, stated rather than implied: a check with no where-tree has
// no narrowing to relax, so there is no probe to run and calibration rests on the author.
//
// EVERY ERROR RETURNED HERE WRAPS EXACTLY ONE OF THREE SENTINELS, so a consumer
// classifies on errors.Is and never on wording. The message text is payload to
// relay, not a contract to pin.

package corpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// The three error classes. They exist because materialization and validation
// failures demand OPPOSITE operator responses — fix your environment versus fix
// your check — and this function knows which one happened; returning one
// undifferentiated error would destroy that knowledge at the return statement.
var (
	// ErrFixtureMaterialization is every failure to PLACE a fixture where the
	// walk can read it, or to observe the walk having read it.
	ErrFixtureMaterialization = errors.New("corpus: fixture could not be materialized")
	// ErrFixtureValidation is every failure that is about the CHECK or the
	// FIXTURE CONTENT — a body that will not compile, a direction that came out
	// wrong, a snippet that only parses under error recovery, a good example
	// that calibrates nothing.
	ErrFixtureValidation = errors.New("corpus: fixture validation failed")
	// ErrNoExecutor is a check type this package cannot validate.
	ErrNoExecutor = errors.New("corpus: this package has no validator for this check type")
)

// Fixture is one example node reduced to what the gate needs: its identity, so
// a refusal can name it, and its source text.
type Fixture struct {
	ID      string
	Content string
}

// ValidateFixtures runs c against its two examples and returns nil only when
// the check FIRES on bad and is SILENT on good.
//
// ValidateFixtures is exported and takes a Check VALUE, so a caller may hand it
// a Check that never passed through ParseCheck. The body-compile failures
// ParseCheck would normally have caught first are therefore re-checked here and
// classified, rather than assumed away.
//
// It handles ast_pattern checks only. Every other declared check type returns
// ErrNoExecutor: a graph assertion's fixture semantics are "does this snippet's
// GRAPH violate the assertion", which needs a populated graph rather than a
// pattern walk, and that validator lives in the package that owns those
// semantics.
func ValidateFixtures(ctx context.Context, c Check, bad, good Fixture) error {
	if c.Type != CheckAstPattern {
		return fmt.Errorf("corpus: %s=%q: this validator handles %s checks only: %w", MetaCheckType, c.Type, CheckAstPattern, ErrNoExecutor)
	}
	pat, err := ast.Parse(c.Pattern)
	if err != nil {
		return fmt.Errorf("corpus: %s does not parse: %v: %w", MetaDSLPattern, err, ErrFixtureValidation)
	}
	cp, err := ast.Compile(pat, c.Language, "")
	if err != nil {
		return fmt.Errorf("corpus: %s does not compile for %s=%q: %v: %w", MetaDSLPattern, MetaLanguage, c.Language, err, ErrFixtureValidation)
	}
	defer cp.Close()
	where, err := ast.ParseWhere(c.Where)
	if err != nil {
		return fmt.Errorf("corpus: %s does not parse: %v: %w", MetaCheckWhere, err, ErrFixtureValidation)
	}
	if err := ast.ValidateWhereKinds(where, c.Language); err != nil {
		return fmt.Errorf("corpus: %s names a kind the %s=%q grammar does not have: %v: %w", MetaCheckWhere, MetaLanguage, c.Language, err, ErrFixtureValidation)
	}

	// Both fixtures are counted BEFORE either direction is judged, so a refusal
	// in either direction can report both counts. A reader diagnosing "silent on
	// bad" needs to know whether the check fired on good too — a check that
	// matches neither is a different defect from one that matches both.
	badTally, err := countFixture(ctx, c.Language, cp, where, bad)
	if err != nil {
		return err
	}
	goodTally, err := countFixture(ctx, c.Language, cp, where, good)
	if err != nil {
		return err
	}
	if badTally.Total == 0 {
		return fmt.Errorf("corpus: the check is SILENT on its bad example %q (bad matched %d, good matched %d): %w", bad.ID, badTally.Total, goodTally.Total, ErrFixtureValidation)
	}
	if goodTally.Total != 0 {
		return fmt.Errorf("corpus: the check FIRES on its good example %q (bad matched %d, good matched %d): %w", good.ID, badTally.Total, goodTally.Total, ErrFixtureValidation)
	}
	return calibrateGoodFixture(ctx, c, cp, where, good)
}

// calibrateGoodFixture re-runs the good example with the narrowing dropped and
// requires it to fire. A good example that stays silent even unnarrowed is
// outside the check's shape population altogether, so its silence under the
// check tested nothing.
//
// A check with no where-tree has nothing to relax and returns nil here — the
// honest limit named in this file's header, not a probe quietly skipped.
func calibrateGoodFixture(ctx context.Context, c Check, cp *ast.CompiledPattern, where *ast.WhereNode, good Fixture) error {
	if where == nil {
		return nil
	}
	relaxed, err := countFixture(ctx, c.Language, cp, nil, good)
	if err != nil {
		return err
	}
	if relaxed.Total == 0 {
		return fmt.Errorf("corpus: the good example %q is silent even with %s dropped, so it sits outside the check's shape population and calibrates nothing: %w", good.ID, MetaCheckWhere, ErrFixtureValidation)
	}
	return nil
}

// countFixture materializes one fixture and runs the compiled pattern over it.
//
// The walk is run with LiftExclusions so discovery's own rule chain — oversize
// files, generated-code paths, vendored paths — cannot silently decline a
// fixture an author deliberately wrote; a declined fixture would read exactly
// like a check that does not fire.
func countFixture(ctx context.Context, lang treesitter.Language, cp *ast.CompiledPattern, where *ast.WhereNode, f Fixture) (ast.CountTally, error) {
	name, ok := treesitter.FixtureFileName(lang)
	if !ok {
		// Defensive and untested by design: every language ast can compile a
		// pattern for is one FixtureFileName resolves, so ast.Compile above
		// refuses first and nothing reaches here.
		return ast.CountTally{}, fmt.Errorf("corpus: no fixture filename for %s=%q: %w", MetaLanguage, lang, ErrFixtureMaterialization)
	}
	dir, err := os.MkdirTemp("", "corpus-fixture-*")
	if err != nil {
		return ast.CountTally{}, fmt.Errorf("corpus: temp directory for fixture %q: %v: %w", f.ID, err, ErrFixtureMaterialization)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(f.Content), 0o600); err != nil {
		return ast.CountTally{}, fmt.Errorf("corpus: write fixture %q: %v: %w", f.ID, err, ErrFixtureMaterialization)
	}
	tally, stats, err := ast.Count(ctx, dir, lang, cp, where, ast.Scope{LiftExclusions: true, IncludeTests: true})
	if err != nil {
		// The pattern already compiled, so a walk error here is the walk's
		// environment — an unreadable directory, a cancelled context — rather
		// than a statement about the check.
		return ast.CountTally{}, fmt.Errorf("corpus: walk over fixture %q: %v: %w", f.ID, err, ErrFixtureMaterialization)
	}
	if stats.FilesScanned != 1 {
		return ast.CountTally{}, fmt.Errorf("corpus: fixture %q was not scanned (files_scanned=%d): %w", f.ID, stats.FilesScanned, ErrFixtureMaterialization)
	}
	if stats.FilesWithParseErrors != 0 {
		return ast.CountTally{}, fmt.Errorf("corpus: fixture %q parses only under error recovery (files_with_parse_errors=%d) — evidence read off a recovered tree is not evidence, so make the snippet parse cleanly: %w", f.ID, stats.FilesWithParseErrors, ErrFixtureValidation)
	}
	return tally, nil
}
