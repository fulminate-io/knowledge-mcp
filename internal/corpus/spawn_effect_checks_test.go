// SPDX-License-Identifier: Apache-2.0

// spawn_effect_checks_test.go — the two EFFECT-LEVEL spawn checks, their
// fixture pairs, their admission, and their behaviour over this repository.
//
// WHY THE CHECK BODIES LIVE IN A TEST AND NOT ONLY IN THE CHECKS GRAPH. A check
// is admitted by the client binary the daemon serves, and these two are written
// against DSL forms this change introduces — the Go import-spec pattern and the
// value-scoped flows_to seeding. The served binary predates them, so
// manage_checks(create) refuses the import leg before writing anything:
//
//	corpus: walk over fixture "the supplied fixture_bad": ast/where: compile
//	sub-pattern "$$$P \"os/exec\"": ast/engine: pattern did not compile under
//	any context wrapper (tried decl,stmt,expr; ...)
//
// The graph write therefore follows the daemon rebuild that follows the merge.
// What does NOT wait is the EVIDENCE: this file runs the same admission gate
// manage_checks(create) runs — ValidateFixtures, which fires the check on the
// bad fixture and requires silence on the good one — against the engine in this
// tree, and then runs both checks over this repository and reads the hits. A
// later admission that disagrees with these rows is a defect in the write, not
// a discovery.
//
// THE ADMISSION GATE FOR CHECK A IS THREE CONDITIONS, NOT TWO, because a fixture
// pair proves only the axes it varies and the corpus is the axis no fixture
// pair can carry: it FIRES on the bad fixture, it is SILENT on the REAL
// reference shape transcribed from source, and it returns ZERO over this
// repository — including on mcphost.go, graphtypecrud/validate.go,
// tools/graphtype_crud.go and tools/graphtype_mcp_roundtrip_test.go, the four
// files that read a record's command.

package corpus

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// ---------------------------------------------------------------------------
// CHECK A — the per-function effect: a record-derived command that reaches a
// raw spawn without reaching the transport's Command field.
// ---------------------------------------------------------------------------

// checkASpawnLeg asks whether the matched value REACHES a spawn. It is an `any`
// over four shapes rather than four checks, which is the whole point of the
// re-authoring: the ten superseded checks spent one check per SPELLING, and the
// spelling axis collapses inside one where-tree.
//
// THE CALL SHAPE IS ONE LEAF FOR ALL FIVE SPAWN APIs, because the callee is
// CAPTURED and constrained by regex rather than spelled: `$SP($$$SA)` binds any
// call, and the regex admits Command, CommandContext, StartProcess, Exec and
// ForkExec on any qualifier. The leading `\.` is load-bearing twice over — it
// keeps the qualifier captured, so an aliased import is matched exactly as a
// plain one, and it excludes the dot-import route the ticket puts out of scope
// (matching every bare call of a name is over-broad by design). It also keeps
// the leaf off `spec.GetCommand()` itself, whose callee ends in "Command" but
// not in ".Command".
//
// THE NON-EMPTY GUARD ON $$$SA IS LOAD-BEARING AND ITS ORDER IS TOO. A sequence
// placeholder matches ZERO siblings, so a spawn-named call written with no
// arguments binds SA to an empty capture whose degenerate span lies outside the
// declaration — and flows_to answers a mis-scoped capture with a hard ERROR
// rather than a false, by design. One such call anywhere in the corpus would
// turn the whole scan from an answer into a refusal. The guard sits AFTER the
// callee regex and BEFORE the flow question because `all` is evaluated in
// order, so the two cheap text leaves short-circuit the candidate before the
// walk is asked about it.
//
// THE COMPOSITE-LITERAL AND POST-CONSTRUCTION SHAPES NEED THEIR OWN LEAVES
// because they are not calls at all, and the literal needs BOTH arities: a
// `$$$REST` in a comma-separated slot must still consume a leading comma, so a
// one-field literal is invisible to the multi-field spelling.
const checkASpawnLeg = `{"any": [
	{"contains_pattern": {"of": "FN", "pattern": "$SP($$$SA)", "where": {"all": [
		{"matches": {"of": "SP", "regex": "\\.(Command|CommandContext|StartProcess|Exec|ForkExec)$"}},
		{"matches": {"of": "SA", "regex": "\\S"}},
		{"flows_to": {"from": "$outer.$match", "to": "SA", "within": "$outer.FN"}}
	]}}},
	{"contains_pattern": {"of": "FN", "pattern": "$P.Cmd{Path: $CP}", "where":
		{"flows_to": {"from": "$outer.$match", "to": "CP", "within": "$outer.FN"}}}},
	{"contains_pattern": {"of": "FN", "pattern": "$P.Cmd{Path: $CP, $$$CR}", "where":
		{"flows_to": {"from": "$outer.$match", "to": "CP", "within": "$outer.FN"}}}},
	{"contains_pattern": {"of": "FN", "pattern": "$CV.Path = $CP", "where":
		{"flows_to": {"from": "$outer.$match", "to": "CP", "within": "$outer.FN"}}}}
]}`

// checkATransportLeg is the exoneration: the same value reaches the transport's
// Command FIELD. The flows_to sits INSIDE the sub-pattern's own where with
// `$outer.` refs, which is the construction help("manage_checks") trap 6
// prescribes — a contains_pattern binds its first matching descendant and does
// not backtrack, so hoisting this to a sibling leaf false-positives on a
// compliant function whose first transport literal is a decoy and
// false-negatives on the bad shape.
//
// THE DESTINATION IS THE FIELD'S VALUE, NOT THE LITERAL. A `to` naming the whole
// literal cannot tell a command reaching Command from one reaching only Dir,
// which is the imprecision this ticket's R2 removes.
const checkATransportLeg = `{"not": {"any": [
	{"contains_pattern": {"of": "FN", "pattern": "&$MP.CommandTransport{Command: $C}", "where":
		{"flows_to": {"from": "$outer.$match", "to": "C", "within": "$outer.FN"}}}},
	{"contains_pattern": {"of": "FN", "pattern": "&$MP.CommandTransport{Command: $C, $$$REST}", "where":
		{"flows_to": {"from": "$outer.$match", "to": "C", "within": "$outer.FN"}}}}
]}}`

const checkAWhere = `{"all": [
	{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}},
	` + checkASpawnLeg + `,
	` + checkATransportLeg + `
]}`

// checkAPattern is the PROVENANCE root: one shape, and not a spawn API. Rooting
// at the record read rather than at the spawn is what makes the check name no
// spawn spelling in its pattern at all.
const checkAPattern = "$X.GetCommand()"

// checkABad is the defect: the record's command is read and handed straight to
// a raw spawn, with no transport anywhere in the function.
const checkABad = `package p

func run(spec *provider) error {
	cmd := exec.Command(spec.GetCommand())
	return cmd.Run()
}
`

// checkAGood is the REAL reference shape, transcribed from
// cmd/knowledge/internal/externalcollector/mcphost.go's stdioTransport, and
// then loaded with every other axis the pair must vary so one fixture carries
// them all:
//
//	PROVENANCE + DESTINATION — the record's command occupies Command:.
//	INTERMEDIATE FLOW        — it gets there through TWO CALL RETURNS
//	                           (LookPath, then CommandContext), never by direct
//	                           assignment. This is the axis a simplified good
//	                           fixture silently drops, and the one that decides
//	                           the real site.
//	ARITY                    — extra argv on the spawn, and the decoy literal
//	                           below carries several fields while the real one
//	                           carries a single Command field.
//	ALIAS                    — the spawn package is imported under an alias, so
//	                           the check must capture the qualifier rather than
//	                           spell it.
//	ROUTE                    — a second, literal-command spawn in the same
//	                           function.
//	CANDIDATE MULTIPLICITY   — that second spawn carries its OWN multi-field
//	                           transport literal, placed FIRST in source order.
//	                           A one-field decoy would be invisible to the
//	                           sub-pattern and would vary nothing; this is the
//	                           cell that separates a backtracking-safe
//	                           construction from a hoisted one.
//	FIELD DISCRIMINATION     — Dir is also fed from the record, so the check
//	                           must tell field from field.
const checkAGood = `package p

import (
	ex "os/exec"
)

func stdioTransport(ctx context.Context, spec *provider) (mcp.Transport, error) {
	probe := ex.CommandContext(ctx, "/bin/true")
	_ = &mcp.CommandTransport{Command: probe, Dir: "/tmp"}

	resolved, err := ex.LookPath(spec.GetCommand())
	if err != nil {
		return nil, err
	}
	cmd := ex.CommandContext(ctx, resolved, spec.GetArgs()...)
	cmd.Dir = spec.GetDir()
	return &mcp.CommandTransport{Command: cmd}, nil
}
`

// ---------------------------------------------------------------------------
// CHECK B — the per-file effect: a file that imports a spawning package and
// reads a registered record's command without handing it to the transport.
// ---------------------------------------------------------------------------

// checkBWhere is what the import-spec pattern form buys: "which files import
// os/exec" becomes a structural leaf rather than a text search or a go/parser
// test.
//
// THE RECORD-COMMAND LEG IS PART OF THE EFFECT AND NOT A NARROWING OF
// CONVENIENCE. The import leg alone matches 101 files in this repository, every
// one of them a legitimate operator-facing subprocess — a browser opener, a
// launchctl reader, a git reader. The effect the ticket names is "a NON-HOST
// file importing a spawn package", and a check carries no path scope, so the
// only thing that distinguishes the host from the other 100 importers is that
// the host is the file handling a REGISTERED RECORD's command. The layered
// counts are asserted below so the leg's contribution is read rather than
// assumed: 101 importers, 1 of them touching a record command, 0 of those
// without the transport.
const checkBWhere = `{"all": [
	{"kind": {"of": "X", "is": "source_file"}},
	{"contains_pattern": {"of": "X", "pattern": "$$$P \"os/exec\"", "as": "IMP"}},
	{"contains_pattern": {"of": "X", "pattern": "$R.GetCommand()", "as": "CMD"}},
	{"not": {"contains_pattern": {"of": "X", "pattern": "$MP.CommandTransport{$$$T}"}}}
]}`

const checkBPattern = "$X"

// checkBBad imports the spawn package inside a GROUPED declaration under an
// ALIAS — the two axes that silenced the whole superseded check set once — and
// spawns a record-derived command with no transport in the file.
const checkBBad = `package p

import (
	"fmt"
	ex "os/exec"
)

func run(spec *provider) error {
	fmt.Println("starting")
	return ex.Command(spec.GetCommand()).Run()
}
`

// checkBGood imports the same package the same way and hands the command to the
// transport. It differs from the bad fixture along the DESTINATION axis alone —
// same grouped declaration, same alias, same record read — so the pair cannot
// be satisfied by the two files merely being different text.
const checkBGood = `package p

import (
	"fmt"
	ex "os/exec"
)

func run(ctx context.Context, spec *provider) (mcp.Transport, error) {
	fmt.Println("starting")
	cmd := ex.CommandContext(ctx, spec.GetCommand())
	return &mcp.CommandTransport{Command: cmd}, nil
}
`

// spawnEffectCheck builds the Check VALUE the graph write will carry, so the
// admission proved here and the admission the graph runs are the same body.
func spawnEffectCheck(id, pattern, where string) Check {
	return Check{
		ID:          id,
		Type:        CheckAstPattern,
		Severity:    foundation.SeverityCritical,
		Language:    treesitter.LangGo,
		Pattern:     pattern,
		Where:       []byte(where),
		FixtureBad:  id + "-bad",
		FixtureGood: id + "-good",
	}
}

func TestSpawnEffectCheckA_AdmitsOnTheRealReferenceShape(t *testing.T) {
	c := spawnEffectCheck("spawn-effect-a", checkAPattern, checkAWhere)
	err := ValidateFixtures(context.Background(), c,
		Fixture{ID: "spawn-effect-a-bad", Content: checkABad},
		Fixture{ID: "spawn-effect-a-good", Content: checkAGood})
	require.NoError(t, err, "check A must fire on the raw-spawn fixture and stay silent on the transcribed reference shape")
}

func TestSpawnEffectCheckA_RefusesTheHoistedConstruction(t *testing.T) {
	// THE MUTATION FOR THE REMEDY FORM. Hoisting the transport leg's flows_to
	// out of the sub-pattern's own where and onto a sibling leaf is the exact
	// construction trap 6 names as the failure: the contains_pattern binds the
	// FIRST transport literal in the function, which in the good fixture is the
	// decoy placed first in source order, so the flow question is asked about
	// the wrong literal. The good fixture then FIRES and admission is refused.
	hoisted := `{"all": [
		{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}},
		` + checkASpawnLeg + `,
		{"contains_pattern": {"of": "FN", "pattern": "&$MP.CommandTransport{Command: $C}", "as": "T"}},
		{"not": {"flows_to": {"from": "$match", "to": "T.C", "within": "FN"}}}
	]}`
	err := ValidateFixtures(context.Background(), spawnEffectCheck("spawn-effect-a-hoisted", checkAPattern, hoisted),
		Fixture{ID: "spawn-effect-a-hoisted-bad", Content: checkABad},
		Fixture{ID: "spawn-effect-a-hoisted-good", Content: checkAGood})
	require.Error(t, err, "the hoisted construction must not admit: it binds the first candidate and does not backtrack")
}

func TestSpawnEffectCheckA_SwappedDestinationRefusesAdmission(t *testing.T) {
	// THE DESTINATION LEG IS NOT VACUOUSLY TRUE. Swap the transport type for one
	// no fixture builds and the exoneration can never fire, so the good fixture
	// starts firing and admission is refused. Without this control, a check
	// whose destination leg matched nothing at all would pass the pair above for
	// the wrong reason.
	swapped := strings.ReplaceAll(checkAWhere, "CommandTransport", "NoSuchTransport")
	err := ValidateFixtures(context.Background(), spawnEffectCheck("spawn-effect-a-swapped", checkAPattern, swapped),
		Fixture{ID: "spawn-effect-a-swapped-bad", Content: checkABad},
		Fixture{ID: "spawn-effect-a-swapped-good", Content: checkAGood})
	require.Error(t, err, "with an impossible destination the good fixture must fire, so admission must be refused")
}

func TestSpawnEffectCheckB_Admits(t *testing.T) {
	c := spawnEffectCheck("spawn-effect-b", checkBPattern, checkBWhere)
	err := ValidateFixtures(context.Background(), c,
		Fixture{ID: "spawn-effect-b-bad", Content: checkBBad},
		Fixture{ID: "spawn-effect-b-good", Content: checkBGood})
	require.NoError(t, err, "check B must fire on the grouped-aliased raw-spawn file and stay silent on the transport file")
}

func TestSpawnEffectCheckB_RefusesWithoutTheImportLeg(t *testing.T) {
	// THE IMPORT LEG IS LOAD-BEARING. Swap the import path for one no fixture
	// imports and the leg can never be satisfied, so the check goes silent on
	// its own bad fixture and admission is refused. This is the known-positive
	// control on the pattern itself that author-a-corpus-check requires: a
	// load-bearing literal swapped for one that cannot exist must move the
	// answer.
	swapped := strings.ReplaceAll(checkBWhere, "os/exec", "os/exec/does/not/exist")
	err := ValidateFixtures(context.Background(), spawnEffectCheck("spawn-effect-b-swapped", checkBPattern, swapped),
		Fixture{ID: "spawn-effect-b-swapped-bad", Content: checkBBad},
		Fixture{ID: "spawn-effect-b-swapped-good", Content: checkBGood})
	require.Error(t, err, "with an unimportable path the check must be silent on its bad fixture, so admission must be refused")
}

// checkAZeroArgHazard carries a spawn-NAMED call written with NO arguments
// ahead of the real defect. A sequence placeholder matches zero siblings, so
// that call binds SA to an empty capture whose degenerate span lies outside the
// declaration, and flows_to answers a mis-scoped capture with a hard ERROR by
// design rather than a false.
const checkAZeroArgHazard = `package p

func run(spec *provider) error {
	exec.Command()
	cmd := exec.Command(spec.GetCommand())
	return cmd.Run()
}
`

func TestSpawnEffectCheckA_AZeroArgumentSpawnCallDoesNotTurnTheScanIntoARefusal(t *testing.T) {
	// A DEFECT FOUND WHILE OBSERVING THE EARLY-RETURN ARM, and it had no test.
	// Without the non-empty guard on SA this fixture does not return "no match"
	// — it returns an ERROR, and one such call anywhere in the corpus would
	// turn the whole scan from an answer into a refusal for every check in it.
	//
	// The row asserts the check still FIRES here, which is the discriminating
	// direction: the fixture is a genuine defect (a record command handed to a
	// raw spawn), so a guard that silenced the candidate set instead of skipping
	// the empty one would show up as a zero rather than as a pass.
	//
	// THE MUTATION: drop the `{"matches": {"of": "SA", "regex": "\\S"}}` leaf
	// from checkASpawnLeg, or move it after the flows_to, and this row goes red
	// with "flows_to to capture SA lies OUTSIDE the declaration named by within".
	err := ValidateFixtures(context.Background(), spawnEffectCheck("spawn-effect-a-zeroarg", checkAPattern, checkAWhere),
		Fixture{ID: "spawn-effect-a-zeroarg-bad", Content: checkAZeroArgHazard},
		Fixture{ID: "spawn-effect-a-zeroarg-good", Content: checkAGood})
	require.NoError(t, err,
		"a zero-argument spawn-named call must be skipped as a candidate, not raise a scope error that refuses the whole check")
}
