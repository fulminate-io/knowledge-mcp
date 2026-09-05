// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// ---------------- R1-a: one match is reused ----------------

// TestCreateTicket_LabelOneMatch_ReusedWithoutCreate — R1's reuse arm on the
// CREATE path, which had no label coverage at all before this ticket (every
// pre-existing CreateTicket test scripts an empty label set). The team holds
// "Testing"; the caller sends "testing". Exactly one filtered lookup goes
// out, its single match's id reaches issueCreate, and NO issueLabelCreate is
// sent. issueLabelCreate IS scripted here deliberately: the fake can answer
// it, so a regression fails on the count assertion rather than on a harness
// error.
func TestCreateTicket_LabelOneMatch_ReusedWithoutCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_testing", Name: "Testing", TeamID: "team_uuid_1", TeamKey: "ABC"})
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("testing") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Labels: "testing",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 — an existing label must be REUSED (ops: %v)", got, opsOf(*reqs))
	}
	creates := reqsFor(*reqs, "IssueCreate")
	if len(creates) != 1 {
		t.Fatalf("issueCreate sent %d time(s), want 1 (ops: %v)", len(creates), opsOf(*reqs))
	}
	if got := labelIDsOf(t, creates[0]); len(got) != 1 || got[0] != "label_uuid_testing" {
		t.Errorf("issueCreate input.labelIds = %v, want [label_uuid_testing]", got)
	}
}

// TestCreateTicket_LabelLookup_TrackerDoesTheComparison — the assertion that
// CARRIES R1. Reading only the response cannot tell "the tracker compared
// case-insensitively" from "the client lowercased and compared"; reading the
// OUTGOING request can. The lookup's query text must ask for eqIgnoreCase and
// its variables must carry the caller's RAW spelling.
//
// It also carries an observation from the plan review: removing the Labels
// struct field does NOT force removing the bulk labels selection from the
// team query, because encoding/json drops response keys with no matching
// field — a query still carrying the bulk read would decode and pass every
// other test here. So this asserts on the TeamByKey request AS
// SENT that no labels selection remains, with the lookup request in the same
// run as the known-positive control for "this instrument can see a labels(
// selection at all".
func TestCreateTicket_LabelLookup_TrackerDoesTheComparison(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_testing", Name: "testing", TeamID: "team_uuid_1", TeamKey: "ABC"})
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("Testing") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	if _, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Labels: "Testing",
	}); err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}

	lookups := reqsFor(*reqs, "TeamLabelByName")
	if len(lookups) != 1 {
		t.Fatalf("filtered label lookups = %d, want 1 (ops: %v)", len(lookups), opsOf(*reqs))
	}
	if !strings.Contains(lookups[0].Query, "eqIgnoreCase") {
		t.Errorf("lookup query text does not ask for eqIgnoreCase — the CLIENT would be folding, not the tracker:\n%s", lookups[0].Query)
	}
	if got := lookups[0].Vars["name"]; got != "Testing" {
		t.Errorf("lookup variables.name = %v, want %q verbatim — the caller's spelling must reach the tracker unfolded", got, "Testing")
	}
	if got := lookups[0].Vars["id"]; got != "team_uuid_1" {
		t.Errorf("lookup variables.id = %v, want team_uuid_1 (the team ensureLabels already holds)", got)
	}
	// The create path's team query must no longer carry the bulk label read.
	// The same assertion is made against TeamByID in the two update-path
	// tests: these are two separate query constants and one says nothing
	// about the other.
	assertNoBulkLabelRead(t, *reqs, "TeamByKey")
}

// ---------------- R1-b: zero matches still creates ----------------

// TestCreateTicket_LabelZeroMatches_CreatesOnce — the property pair for the
// reuse test: an adapter that simply stopped creating labels would pass every
// reuse assertion, so a genuinely absent label must still be created exactly
// once, with the caller's spelling, and the created id must reach the write.
func TestCreateTicket_LabelZeroMatches_CreatesOnce(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false) // no matches
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("brand-new-label") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	if _, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Labels: "brand-new-label",
	}); err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 — the create must follow a lookup that found nothing (ops: %v)", got, opsOf(*reqs))
	}
	labelCreates := reqsFor(*reqs, "IssueLabelCreate")
	if len(labelCreates) != 1 {
		t.Fatalf("issueLabelCreate sent %d time(s), want exactly 1 (ops: %v)", len(labelCreates), opsOf(*reqs))
	}
	input, _ := labelCreates[0].Vars["input"].(map[string]any)
	if got := input["name"]; got != "brand-new-label" {
		t.Errorf("issueLabelCreate input.name = %v, want brand-new-label (the caller's spelling)", got)
	}
	if got := input["teamId"]; got != "team_uuid_1" {
		t.Errorf("issueLabelCreate input.teamId = %v, want team_uuid_1", got)
	}
	creates := reqsFor(*reqs, "IssueCreate")
	if len(creates) != 1 {
		t.Fatalf("issueCreate sent %d time(s), want 1 (ops: %v)", len(creates), opsOf(*reqs))
	}
	if got := labelIDsOf(t, creates[0]); len(got) != 1 || got[0] != "label_uuid_created" {
		t.Errorf("issueCreate input.labelIds = %v, want [label_uuid_created]", got)
	}
}

// TestCreateTicket_LabelCreateFails_HardErrorNoIssueCreate — the locked
// hard-error contract (queries_write.go's issueLabelCreateMutation doc
// comment) is unchanged by the lookup: a create that fails names the label
// and NO issue is created. The lookup-count assertion is what makes this
// test observe the NEW path rather than the old bulk-map one.
func TestCreateTicket_LabelCreateFails_HardErrorNoIssueCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false)
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string {
			return `{"data":null,"errors":[{"message":"label create rejected"}]}`
		},
		"IssueCreate": func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Labels: "brand-new-label",
	})
	if err == nil {
		t.Fatalf("expected a hard error from the label-create rejection, got nil (ops: %v)", opsOf(*reqs))
	}
	if !strings.Contains(err.Error(), "brand-new-label") {
		t.Errorf("err = %v, want the error to name the label", err)
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 (ops: %v)", got, opsOf(*reqs))
	}
	// ATTRIBUTION: this test is named for the CREATE failing, so the create
	// must actually have been attempted. Without this the test stays green
	// when the LOOKUP fails instead and no create is ever sent — the error
	// names the label and no ticket lands in that case too.
	if got := countOp(*reqs, "IssueLabelCreate"); got != 1 {
		t.Errorf("issueLabelCreate sent %d time(s), want 1 — this test observes the CREATE-failure arm (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueCreate"); got != 0 {
		t.Errorf("issueCreate sent %d time(s), want 0 — no ticket may land after a label failure (ops: %v)", got, opsOf(*reqs))
	}
}
