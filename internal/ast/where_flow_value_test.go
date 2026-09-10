// SPDX-License-Identifier: Apache-2.0

// where_flow_value_test.go — flows_to's VALUE precision.
//
// The leaf's headline claim is that a value reaches a position. Its `from` is
// a capture, and when that capture is a composite expression the endpoints
// INSIDE it are operand reads rather than the value: `spec.GetCommand()` and
// `spec.GetEnv()` share the operand `spec`, so seeding from the operand and
// then closing over same-text occurrences answers "does this RECEIVER reach the
// destination" — which is true for every method on it. These rows pin the
// difference in both directions, including through the two call returns the
// repository's own reference site actually writes.

package ast

import (
	"testing"
)

// flowRealTransport is mcphost.go's stdioTransport transcribed from source
// (cmd/knowledge/internal/externalcollector/mcphost.go:158-173), with the
// package qualifiers kept because they are load-bearing: the destination
// sub-pattern captures the qualifier, and a fixture that drops it matches
// nothing while reading as a broken check.
//
// THE AXIS THIS FIXTURE CARRIES AND A SIMPLIFIED ONE DOES NOT is INTERMEDIATE
// FLOW: the record's command reaches the transport's Command field through TWO
// CALL RETURNS (LookPath, then CommandContext), never by direct assignment.
const flowRealTransport = `package main

func stdioTransport(ctx context.Context, spec *knowledgev1.StdioProvider) (mcp.Transport, error) {
	resolved, err := exec.LookPath(spec.GetCommand())
	if err != nil {
		return nil, fmt.Errorf("stdio provider command %q is not executable: %w", spec.GetCommand(), err)
	}
	cmd := exec.CommandContext(ctx, resolved, spec.GetArgs()...)
	cmd.Env = allowlistedEnv(spec.GetEnv())
	cmd.Dir = os.TempDir()
	cmd.Stderr = os.Stderr
	return &mcp.CommandTransport{Command: cmd}, nil
}
`

// reachesCommandField is the destination question every row below asks: does
// the matched value reach the Command field of a transport literal built in the
// same function. The flows_to sits INSIDE the sub-pattern's own where with
// `$outer.` refs — the construction help("manage_checks") trap 6 prescribes,
// because a contains_pattern binds its first matching descendant and does not
// backtrack.
func reachesCommandField(destType string) string {
	return `{"all": [
		{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}},
		{"any": [
			{"contains_pattern": {"of": "FN", "pattern": "&$MP.` + destType + `{Command: $C}",
				"where": {"flows_to": {"from": "$outer.$match", "to": "C", "within": "$outer.FN"}}}},
			{"contains_pattern": {"of": "FN", "pattern": "&$MP.` + destType + `{Command: $C, $$$REST}",
				"where": {"flows_to": {"from": "$outer.$match", "to": "C", "within": "$outer.FN"}}}}
		]}
	]}`
}

func TestFlowValue_ReachesTheCommandFieldThroughTwoCallReturns(t *testing.T) {
	// The reference site's own shape. `spec.GetCommand()` at the LookPath call
	// really does reach the transport's Command field — through `resolved`,
	// then through `cmd` — so the leaf must say so. The second GetCommand read,
	// inside the error message, reaches nothing and must not.
	got, err := runWhere(t, "$X.GetCommand()", flowRealTransport, reachesCommandField("CommandTransport"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 — the LookPath read reaches Command; the error-message read does not", got)
	}
}

func TestFlowValue_ASiblingAccessorOnTheSameReceiverDoesNotReachIt(t *testing.T) {
	// THE ROW THE VALUE SCOPING EXISTS FOR, and the discriminating control on
	// the row above. `spec.GetEnv()` is handed to allowlistedEnv and assigned
	// to cmd.Env; it never reaches Command. Before the seeding was value-scoped
	// this returned a MATCH, because GetEnv and GetCommand share the operand
	// `spec` and the same-text edge class joined every occurrence of it.
	//
	// THE MUTATION: restore the operand-level seeding in flowReaches and this
	// row goes red while the row above stays green.
	got, err := runWhere(t, "$X.GetEnv()", flowRealTransport, reachesCommandField("CommandTransport"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 0 {
		t.Errorf("matches = %d, want 0 — GetEnv's value reaches cmd.Env, never Command", got)
	}
}

func TestFlowValue_SwappedDestinationReturnsZero(t *testing.T) {
	// Row 10. THE NEGATIVE CONTROL, same run, same fixture: a destination type
	// that cannot exist returns nothing, so the destination leg constrains the
	// answer rather than being vacuously true.
	got, err := runWhere(t, "$X.GetCommand()", flowRealTransport, reachesCommandField("NoSuchTransportType"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 0 {
		t.Errorf("matches = %d, want 0 for a destination type no fixture builds", got)
	}
}

// flowTwoFields carries two record-derived values into ONE literal, one into
// Command and one into Dir. It is the field-versus-field case: the whole
// literal is a single destination to a coarse leaf, and the two values are
// distinguishable only when the `to` capture names the FIELD's value.
const flowTwoFields = `package main

func build(spec *P) *mcp.CommandTransport {
	dir := spec.GetDir()
	cmd := spec.GetCommand()
	return &mcp.CommandTransport{Command: cmd, Dir: dir}
}
`

func TestFlowValue_FieldLevelDestinationSeparatesCommandFromDir(t *testing.T) {
	// Row 9. Both candidate literals are the same literal here, so the single
	// match is a DISCRIMINATION between two values reaching two fields of it,
	// not a difference in which literal was walked.
	cmdHits, err := runWhere(t, "$X.GetCommand()", flowTwoFields, reachesCommandField("CommandTransport"))
	if err != nil {
		t.Fatalf("GetCommand err: %v", err)
	}
	if cmdHits != 1 {
		t.Errorf("GetCommand matches = %d, want 1", cmdHits)
	}

	dirHits, err := runWhere(t, "$X.GetDir()", flowTwoFields, reachesCommandField("CommandTransport"))
	if err != nil {
		t.Fatalf("GetDir err: %v", err)
	}
	if dirHits != 0 {
		t.Errorf("GetDir matches = %d, want 0 — its value reaches Dir, not Command", dirHits)
	}
}

func TestFlowValue_WholeLiteralDestinationCannotSeparateTheTwoFields(t *testing.T) {
	// The same fixture asked the COARSE way — `to` naming the whole literal
	// rather than a field's value — answers YES for both, which is the
	// imprecision R2 removes. Asserting it here keeps the row above from being
	// read as a property of the fixture rather than of the destination capture.
	coarse := `{"all": [
		{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}},
		{"contains_pattern": {"of": "FN", "pattern": "&$MP.CommandTransport{$$$T}",
			"where": {"flows_to": {"from": "$outer.$match", "to": "T", "within": "$outer.FN"}}}}
	]}`
	for _, accessor := range []string{"$X.GetCommand()", "$X.GetDir()"} {
		got, err := runWhere(t, accessor, flowTwoFields, coarse)
		if err != nil {
			t.Fatalf("%s err: %v", accessor, err)
		}
		if got != 1 {
			t.Errorf("%s coarse matches = %d, want 1 (both values reach the literal)", accessor, got)
		}
	}
}

// flowTwoSpawns holds two spawns in one declaration: one whose result reaches
// the transport and one whose result is read locally and never does.
const flowTwoSpawns = `package main

func two(spec *P) *mcp.CommandTransport {
	other := exec.Command(spec.GetProbe())
	_ = other.Run()
	cmd := exec.Command(spec.GetCommand())
	return &mcp.CommandTransport{Command: cmd}
}
`

func TestFlowValue_ASecondSpawnWhoseResultNeverReachesTheDestinationIsNotMatched(t *testing.T) {
	// The ticket's own observable for R2, verbatim: "a second spawn in the same
	// function whose result does not reach the named destination is NOT
	// matched, and one whose result does is."
	reaching, err := runWhere(t, "$X.GetCommand()", flowTwoSpawns, reachesCommandField("CommandTransport"))
	if err != nil {
		t.Fatalf("GetCommand err: %v", err)
	}
	if reaching != 1 {
		t.Errorf("GetCommand matches = %d, want 1", reaching)
	}

	notReaching, err := runWhere(t, "$X.GetProbe()", flowTwoSpawns, reachesCommandField("CommandTransport"))
	if err != nil {
		t.Fatalf("GetProbe err: %v", err)
	}
	if notReaching != 0 {
		t.Errorf("GetProbe matches = %d, want 0 — its spawn's result is run locally and never handed to the transport", notReaching)
	}
}

func TestFlowValue_CrossDeclarationStaysOutOfScopeAndIsPinnedHere(t *testing.T) {
	// Row 12. The walk is intra-declaration BY DESIGN and this ticket does not
	// change that (ticket premise P3, recorded as a limit rather than built).
	// A wrapper carrying the identical shape across a declaration boundary is
	// ABSENT from a run whose same-declaration positive matches — the pair is
	// what makes the absence mean "out of scope" rather than "the fixture is
	// wrong".
	target := `package main

func wrapper(spec *P) *mcp.CommandTransport {
	return handOff(spec.GetCommand())
}

func handOff(c string) *mcp.CommandTransport {
	cmd := exec.Command(c)
	return &mcp.CommandTransport{Command: cmd}
}

func sameDeclaration(spec *P) *mcp.CommandTransport {
	cmd := exec.Command(spec.GetCommand())
	return &mcp.CommandTransport{Command: cmd}
}
`
	got, err := runWhere(t, "$X.GetCommand()", target, reachesCommandField("CommandTransport"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 — the same-declaration positive matches and the cross-declaration wrapper does not", got)
	}
}

func TestFlowValue_FromMatchAndFromCaptureAgreeOnTheReachingValue(t *testing.T) {
	// Row 11. `from` naming the whole matched call and `from` naming an inner
	// capture are two different values, and the row records what each answers
	// as CURRENT BEHAVIOUR so this ticket is not silently credited with more
	// than it changed. On the two-spawn fixture both spellings agree: the
	// reaching spawn matches, the local one does not.
	fromCapture := `{"all": [
		{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}},
		{"contains_pattern": {"of": "FN", "pattern": "&$MP.CommandTransport{Command: $C}",
			"where": {"flows_to": {"from": "$outer.CMD", "to": "C", "within": "$outer.FN"}}}}
	]}`
	got, err := runWhere(t, "exec.Command($CMD)", flowTwoSpawns, fromCapture)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("from:CMD matches = %d, want 1 — only the spawn whose argument reaches Command", got)
	}

	fromMatch := `{"all": [
		{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}},
		{"contains_pattern": {"of": "FN", "pattern": "&$MP.CommandTransport{Command: $C}",
			"where": {"flows_to": {"from": "$outer.$match", "to": "C", "within": "$outer.FN"}}}}
	]}`
	gotMatch, err := runWhere(t, "exec.Command($CMD)", flowTwoSpawns, fromMatch)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotMatch != 1 {
		t.Errorf("from:$match matches = %d, want 1", gotMatch)
	}
}

// flowDirectArgument passes the record's command STRAIGHT into the spawn's
// argument list, with no intermediate binding at all. It is the shape cells 7,
// 8 and 9 of the supersession corpus carry, and the one the traversal cannot
// answer: the `from` capture is a call expression, so it is not a flow endpoint
// and can never be dequeued, while the `to` capture is the argument sequence
// that already CONTAINS it.
const flowDirectArgument = `package main

func direct(spec *P) error {
	return syscall.Exec(spec.GetCommand(), nil, nil)
}
`

func TestFlowValue_AFromThatAlreadyLiesInsideTheDestinationReachesItWithoutTraversing(t *testing.T) {
	// THE `satisfied` EARLY-RETURN ARM, named and observed directly.
	//
	// seedFlowQueue answers this case before the walk starts, because there is
	// nothing to walk: a composite `from` is not an endpoint, so seeding it
	// produces no cursor that could ever be dequeued and compared against `to`.
	// Without the arm this returns 0 and the whole direct-argument spawn shape
	// becomes invisible.
	//
	// THE MUTATION: delete `if satisfied { return true }` from flowReaches and
	// this row goes red. It is asserted here rather than left to
	// TestSupersession_ThePairReproducesEveryCellTheTenFind, which also goes red
	// but reports it as three missing corpus cells — a message that sends a
	// reader to the checks rather than to the seeding.
	reaching := `{"all": [
		{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}},
		{"contains_pattern": {"of": "FN", "pattern": "$SP($$$SA)", "where":
			{"flows_to": {"from": "$outer.$match", "to": "SA", "within": "$outer.FN"}}}}
	]}`
	got, err := runWhere(t, "$X.GetCommand()", flowDirectArgument, reaching)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 — the command IS the spawn's argument, so it reaches it", got)
	}

	// THE DISCRIMINATING CONTROL, same fixture, same run: a destination the
	// value does not lie inside returns 0. Without it, the row above would pass
	// for an arm that returned true unconditionally.
	notReaching := `{"all": [
		{"inside_pattern": {"of": "$match", "pattern": "func $F($$$P) $$$R { $$$B }", "as": "FN"}},
		{"contains_pattern": {"of": "FN", "pattern": "&$MP.CommandTransport{Command: $C}", "where":
			{"flows_to": {"from": "$outer.$match", "to": "C", "within": "$outer.FN"}}}}
	]}`
	absent, err := runWhere(t, "$X.GetCommand()", flowDirectArgument, notReaching)
	if err != nil {
		t.Fatalf("control err: %v", err)
	}
	if absent != 0 {
		t.Errorf("control matches = %d, want 0 — this fixture builds no transport at all", absent)
	}
}
