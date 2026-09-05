// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// R1's REFUSAL and ORDERING arms: what the adapter must decline, and what it
// must have finished resolving before it writes anything. Split out of
// backend_write_labels_test.go, which holds the reuse and create arms, to keep
// both files under the repository's 500-line ceiling. The shared fake and its
// fixtures live in label_lookup_harness_test.go.

// ---------------- R1-c: two or more matches is refused ----------------

// TestCreateTicket_LabelTwoMatches_RefusedNamingEachWithScope — R1's
// ambiguity arm. The refusal must name EVERY match, and the two matches here
// share a name (a team label and a workspace label, which is exactly the case
// R1 names), so a refusal rendered from the name alone would print the same
// string twice and tell the caller nothing. Each match is therefore named
// with its SCOPE: the team key, or workspace for a row Linear returns with
// team: null.
//
// REACHABILITY IS REASONED, NOT MEASURED: the validator's close-out sweep
// found 0 of 330 folded names on the live team carrying more than one label,
// and constructing the state needs a label create no read-only lane may make.
// This fixture is scripted by necessity; do not read it as a reproduced state.
func TestCreateTicket_LabelTwoMatches_RefusedNamingEachWithScope(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_team", Name: "Bug", TeamID: "team_uuid_1", TeamKey: "ABC"},
				labelMatch{ID: "label_uuid_workspace", Name: "Bug"}, // team: null → workspace-scoped
			)
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("Bug") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Labels: "Bug",
	})
	if err == nil {
		t.Fatalf("expected a refusal for an ambiguous label, got nil (ops: %v)", opsOf(*reqs))
	}
	msg := err.Error()
	for _, want := range []string{"Bug", "label_uuid_team", "label_uuid_workspace", "ABC", "workspace"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must name every match with its scope; %q missing from: %s", want, msg)
		}
	}
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if be.Reason != backends.ReasonInvalidArgument || be.Transient {
		t.Errorf("typed wrap misclassified: Reason=%q Transient=%v, want %q/false — an ambiguous name is bad input",
			be.Reason, be.Transient, backends.ReasonInvalidArgument)
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 — the refusal comes BEFORE any create (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueCreate"); got != 0 {
		t.Errorf("issueCreate sent %d time(s), want 0 — no ticket may land (ops: %v)", got, opsOf(*reqs))
	}
}

// TestCreateTicket_LabelManyMatches_RefusalSaysMoreExist — second observation
// on the ambiguity branch. R1 says the refusal names EVERY match; the lookup
// asks for a bounded page, so a response whose pageInfo.hasNextPage is true
// means the refusal's list is INCOMPLETE. It must say so, or a truncated list
// reads as a complete one. Paired with the two-row case above, where
// hasNextPage is false and no truncation notice appears.
func TestCreateTicket_LabelManyMatches_RefusalSaysMoreExist(t *testing.T) {
	many := make([]labelMatch, 0, 10)
	for i := range 10 {
		many = append(many, labelMatch{
			ID: fmt.Sprintf("label_uuid_%d", i), Name: "Bug",
			TeamID: "team_uuid_1", TeamKey: fmt.Sprintf("T%d", i),
		})
	}
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(true, many...)
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("Bug") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Labels: "Bug",
	})
	if err == nil {
		t.Fatalf("expected a refusal for an ambiguous label, got nil (ops: %v)", opsOf(*reqs))
	}
	msg := err.Error()
	if !strings.Contains(msg, "further matches") {
		t.Errorf("a truncated match list must say further matches exist; got: %s", msg)
	}
	if !strings.Contains(msg, "label_uuid_0") || !strings.Contains(msg, "label_uuid_9") {
		t.Errorf("refusal must still name the matches it DID read; got: %s", msg)
	}
	if got := countOp(*reqs, "IssueCreate"); got != 0 {
		t.Errorf("issueCreate sent %d time(s), want 0 (ops: %v)", got, opsOf(*reqs))
	}
}

// TestCreateTicket_LabelTwoMatches_NoTruncationNotice — the other half of the
// pair above: an exhaustive two-row refusal must NOT claim there are more.
func TestCreateTicket_LabelTwoMatches_NoTruncationNotice(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_team", Name: "Bug", TeamID: "team_uuid_1", TeamKey: "ABC"},
				labelMatch{ID: "label_uuid_workspace", Name: "Bug"},
			)
		},
		"IssueCreate": func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Labels: "Bug",
	})
	if err == nil {
		t.Fatalf("expected a refusal, got nil (ops: %v)", opsOf(*reqs))
	}
	if strings.Contains(err.Error(), "further matches") {
		t.Errorf("a complete two-match refusal must not claim more exist; got: %s", err.Error())
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 — the refusal must come from the lookup (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueCreate"); got != 0 {
		t.Errorf("issueCreate sent %d time(s), want 0 (ops: %v)", got, opsOf(*reqs))
	}
}

// ---------------- R1: resolve every name before creating any ----------------

// TestCreateTicket_AmbiguityAfterAnAbsentName_RefusesWithNothingCreated —
// R1's "before any create" means before ANY create at all, not merely before
// the create of the ambiguous name itself.
//
// The list is "brand-new,Bug": the FIRST name is genuinely absent and the
// SECOND is ambiguous. Resolving and creating name-by-name would create
// brand-new on the team and only then refuse, leaving a label written on the
// tracker with no ticket landed — a partial write of exactly the kind R2 and
// the ticket's premise P12 forbid, and one the locked no-cleanup contract
// means nobody removes.
func TestCreateTicket_AmbiguityAfterAnAbsentName_RefusesWithNothingCreated(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, vars map[string]any) string {
			if name, _ := vars["name"].(string); name == "Bug" {
				return labelLookupBody(false,
					labelMatch{ID: "label_uuid_team", Name: "Bug", TeamID: fixtureTeamID, TeamKey: fixtureTeamKey},
					labelMatch{ID: "label_uuid_workspace", Name: "Bug"},
				)
			}
			return labelLookupBody(false) // "brand-new" is absent
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("brand-new") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "brand-new,Bug",
	})
	if err == nil {
		t.Fatalf("expected a refusal for the ambiguous second name, got nil (ops: %v)", opsOf(*reqs))
	}
	var amb *ErrAmbiguousLabel
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want *ErrAmbiguousLabel", err)
	}
	if amb.Requested != "Bug" {
		t.Errorf("refusal names %q, want Bug", amb.Requested)
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 2 {
		t.Errorf("filtered label lookups = %d, want 2 — every name is resolved before anything is created (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 — a label created before the refusal is a partial write nobody cleans up (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueCreate"); got != 0 {
		t.Errorf("issueCreate sent %d time(s), want 0 (ops: %v)", got, opsOf(*reqs))
	}
}

// TestCreateTicket_AllNamesResolvedBeforeAnyCreate_OrderPreserved — the other
// half of the settlement, and the one that shows the resolve-all pass is a
// SEQUENCE and not just a count. With "brand-new,tools" (absent, then held)
// every lookup must precede every create, exactly one label is created, and
// issueCreate carries both ids in the caller's declared order.
//
// Counting alone cannot see this: the name-by-name shape issues the same two
// lookups and the same one create, just interleaved. The ordering assertion is
// what separates them.
func TestCreateTicket_AllNamesResolvedBeforeAnyCreate_OrderPreserved(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, vars map[string]any) string {
			if name, _ := vars["name"].(string); name == "tools" {
				return labelLookupBody(false,
					labelMatch{ID: "label_uuid_tools", Name: "tools", TeamID: fixtureTeamID, TeamKey: fixtureTeamKey})
			}
			return labelLookupBody(false) // "brand-new" is absent
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("brand-new") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	if _, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "brand-new,tools",
	}); err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 2 {
		t.Fatalf("filtered label lookups = %d, want 2 (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 1 {
		t.Fatalf("issueLabelCreate sent %d time(s), want 1 — only the absent name is created (ops: %v)", got, opsOf(*reqs))
	}
	// THE SEQUENCE: every lookup precedes every create.
	lastLookup := lastIndexOfOp(*reqs, "TeamLabelByName")
	firstCreate := firstIndexOfOp(*reqs, "IssueLabelCreate")
	if lastLookup > firstCreate {
		t.Errorf("a label was created at position %d before the last lookup at position %d; "+
			"every requested name must be resolved before anything is created (ops: %v)",
			firstCreate, lastLookup, opsOf(*reqs))
	}
	creates := reqsFor(*reqs, "IssueCreate")
	if len(creates) != 1 {
		t.Fatalf("issueCreate sent %d time(s), want 1 (ops: %v)", len(creates), opsOf(*reqs))
	}
	got := labelIDsOf(t, creates[0])
	want := []string{"label_uuid_created", "label_uuid_tools"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("issueCreate input.labelIds = %v, want %v in the caller's declared order", got, want)
	}
}

// ---------------- R1: a name repeated in one list is ONE label ----------------

// TestCreateTicket_RepeatedLabelName_OneLookupOneCreateAndLands — a name the
// caller writes twice is one label, so it is looked up once and created at
// most once.
//
// WHY THE FAKE REJECTS THE SECOND CREATE: that is what the tracker does. The
// ticket's reproduced premise P4 is that creating a label the team already
// holds is rejected by name. So an adapter that creates once per OCCURRENCE
// writes the label, then hard-errors on its own second create, and no ticket
// lands — leaving a label on the tracker that nothing removes. That is the
// partial write the resolve-all pass exists to prevent, reached by a different
// road.
func TestCreateTicket_RepeatedLabelName_OneLookupOneCreateAndLands(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false) // "dup" is absent
		},
		"IssueLabelCreate": func(callN int, _ map[string]any) string {
			if callN > 0 {
				// The tracker's duplicate rejection, premise P4.
				return `{"data":null,"errors":[{"message":"A label with this name already exists"}]}`
			}
			return issueLabelCreateBody("dup")
		},
		"IssueCreate": func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "dup,dup",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 — the same name twice is one label (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 1 {
		t.Errorf("issueLabelCreate sent %d time(s), want 1 — a second create of the same name is rejected by the tracker and strands the first (ops: %v)", got, opsOf(*reqs))
	}
	// The sequence still holds: resolution precedes creation.
	if last, first := lastIndexOfOp(*reqs, "TeamLabelByName"), firstIndexOfOp(*reqs, "IssueLabelCreate"); last > first {
		t.Errorf("a label was created at position %d before the last lookup at position %d (ops: %v)", first, last, opsOf(*reqs))
	}
	creates := reqsFor(*reqs, "IssueCreate")
	if len(creates) != 1 {
		t.Fatalf("issueCreate sent %d time(s), want 1 — the ticket must land (ops: %v)", len(creates), opsOf(*reqs))
	}
	// One id per DECLARED occurrence, which is what the base shape sent.
	got := labelIDsOf(t, creates[0])
	want := []string{"label_uuid_created", "label_uuid_created"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("issueCreate input.labelIds = %v, want %v — one id per declared occurrence", got, want)
	}
}

// TestCreateTicket_RepeatedLabelDifferentCase_OneLookupOneCreate — the same
// rule under two spellings. The lookup asks the tracker with eqIgnoreCase, so
// the tracker treats "Dup" and "dup" as one label; this adapter must treat
// them as one request too, or it sends the tracker a create for a name it will
// reject as a duplicate of the one just made.
//
// The FIRST spelling is the one carried, so the request the tracker sees is
// the one the caller wrote first.
func TestCreateTicket_RepeatedLabelDifferentCase_OneLookupOneCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false)
		},
		"IssueLabelCreate": func(callN int, _ map[string]any) string {
			if callN > 0 {
				return `{"data":null,"errors":[{"message":"A label with this name already exists"}]}`
			}
			return issueLabelCreateBody("Dup")
		},
		"IssueCreate": func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	if _, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "Dup,dup",
	}); err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	lookups := reqsFor(*reqs, "TeamLabelByName")
	if len(lookups) != 1 {
		t.Fatalf("filtered label lookups = %d, want 1 — two spellings the tracker folds together are one label (ops: %v)", len(lookups), opsOf(*reqs))
	}
	if got := lookups[0].Vars["name"]; got != "Dup" {
		t.Errorf("lookup variables.name = %v, want %q — the FIRST spelling the caller wrote is the one carried", got, "Dup")
	}
	labelCreates := reqsFor(*reqs, "IssueLabelCreate")
	if len(labelCreates) != 1 {
		t.Fatalf("issueLabelCreate sent %d time(s), want 1 (ops: %v)", len(labelCreates), opsOf(*reqs))
	}
	input, _ := labelCreates[0].Vars["input"].(map[string]any)
	if got := input["name"]; got != "Dup" {
		t.Errorf("issueLabelCreate input.name = %v, want %q — the first spelling", got, "Dup")
	}
}

// TestCreateTicket_EveryNameAbsent_CreatesEachOnceInDeclaredOrder — the
// all-absent multi-name class, which no other test drives: every shipped
// create-arm test has either one name or a mix of held and absent.
//
// It is the arm most exposed to the two-pass split, because it is the only one
// where pass two does real work more than once, and it is where a dedupe that
// over-merged distinct names would show up as a missing create.
func TestCreateTicket_EveryNameAbsent_CreatesEachOnceInDeclaredOrder(t *testing.T) {
	ids := map[string]string{"alpha": "label_uuid_alpha", "beta": "label_uuid_beta", "gamma": "label_uuid_gamma"}
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false) // every name is absent
		},
		"IssueLabelCreate": func(_ int, vars map[string]any) string {
			input, _ := vars["input"].(map[string]any)
			name, _ := input["name"].(string)
			id, ok := ids[name]
			if !ok {
				return `{"data":null,"errors":[{"message":"TEST: unexpected label create for ` + name + `"}]}`
			}
			return `{"data":{"issueLabelCreate":{"issueLabel":{"id":"` + id + `","name":"` + name + `"}}}}`
		},
		"IssueCreate": func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	if _, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "alpha,beta,gamma",
	}); err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 3 {
		t.Errorf("filtered label lookups = %d, want 3 — three distinct names (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 3 {
		t.Errorf("issueLabelCreate sent %d time(s), want 3 — each absent name is created exactly once (ops: %v)", got, opsOf(*reqs))
	}
	// Every lookup still precedes every create, even when pass two does the
	// most work it ever does.
	if last, first := lastIndexOfOp(*reqs, "TeamLabelByName"), firstIndexOfOp(*reqs, "IssueLabelCreate"); last > first {
		t.Errorf("a label was created at position %d before the last lookup at position %d (ops: %v)", first, last, opsOf(*reqs))
	}
	// The creates themselves go out in declared order.
	var createdNames []string
	for _, r := range reqsFor(*reqs, "IssueLabelCreate") {
		input, _ := r.Vars["input"].(map[string]any)
		n, _ := input["name"].(string)
		createdNames = append(createdNames, n)
	}
	if strings.Join(createdNames, "|") != "alpha|beta|gamma" {
		t.Errorf("labels created in order %v, want [alpha beta gamma] — the caller's declared order", createdNames)
	}
	creates := reqsFor(*reqs, "IssueCreate")
	if len(creates) != 1 {
		t.Fatalf("issueCreate sent %d time(s), want 1 (ops: %v)", len(creates), opsOf(*reqs))
	}
	got := labelIDsOf(t, creates[0])
	want := []string{"label_uuid_alpha", "label_uuid_beta", "label_uuid_gamma"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("issueCreate input.labelIds = %v, want %v in declared order", got, want)
	}
}

// TestCreateTicket_RepeatedLabelNonASCIIFold_CollapsesDeliberately — the fold
// axis the ASCII sibling above cannot vary.
//
// "Dup,dup" is pure ASCII, where Go's simple case folding, full folding, an
// ASCII-only fold and a SQL lower() all agree, so that fixture pins the
// mechanism but not the CHOICE of comparator. This one does: U+212A KELVIN
// SIGN folds to 'k' under strings.EqualFold and does NOT under a plain
// lower(), so the two spellings here are one label on this side and may well
// be two on the tracker's.
//
// THE CONTRACT ASSERTED IS THE DELIBERATE ONE: the fold stays Go's, so the
// pair collapses to one lookup and one create. That is a choice, not a
// discovery — the tracker's own rule is the ticket's unverified premise P14
// and settling it needs a case-differing label create no lane here may make.
// The risk it accepts is named in indexOfSameLabel's comment: on a pair the
// tracker would keep apart the caller receives one id where it asked for two.
// It is asserted here so narrowing or widening the fold later fails a test
// with this name rather than surfacing as a tracker rejection in production.
func TestCreateTicket_RepeatedLabelNonASCIIFold_CollapsesDeliberately(t *testing.T) {
	// U+212A KELVIN SIGN, written as an escape so the fixture cannot be
	// flattened to an ASCII K by an editor or a copy-paste.
	const kelvinSign = "\u212Aelvin"
	if !strings.EqualFold("kelvin", kelvinSign) {
		t.Fatalf("fixture control failed: %q and %q are not EqualFold, so this test cannot exercise the fold axis", "kelvin", kelvinSign)
	}
	// CONTROL that the fixture DISCRIMINATES: an ASCII-only fold, which is what
	// narrowing this comparator would mean in practice, does NOT equate the
	// pair. Without this the fixture could pass under both candidate
	// implementations and would pin nothing.
	if asciiFoldEqual("kelvin", kelvinSign) {
		t.Fatalf("fixture control failed: an ASCII-only fold already equates %q and %q, so this fixture does not discriminate the comparator", "kelvin", kelvinSign)
	}

	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false) // absent under either spelling
		},
		"IssueLabelCreate": func(callN int, _ map[string]any) string {
			if callN > 0 {
				return `{"data":null,"errors":[{"message":"A label with this name already exists"}]}`
			}
			return issueLabelCreateBody("kelvin")
		},
		"IssueCreate": func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	if _, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "kelvin," + kelvinSign,
	}); err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	lookups := reqsFor(*reqs, "TeamLabelByName")
	if len(lookups) != 1 {
		t.Fatalf("filtered label lookups = %d, want 1 — the fold is Go's and collapses this pair (ops: %v)", len(lookups), opsOf(*reqs))
	}
	if got := lookups[0].Vars["name"]; got != "kelvin" {
		t.Errorf("lookup variables.name = %q, want %q — the FIRST spelling the caller wrote", got, "kelvin")
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 1 {
		t.Errorf("issueLabelCreate sent %d time(s), want 1 (ops: %v)", got, opsOf(*reqs))
	}
}
