// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// ---------------- R2: a lookup failure is an error naming the label ----------------

// TestCreateTicket_LabelLookupTransportFailure_NamesLabelNoCreate — R2 arm
// (a). The connection is dropped on the lookup only, so team resolution
// succeeds and the failure is unambiguously the lookup's.
func TestCreateTicket_LabelLookupTransportFailure_NamesLabelNoCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, vars map[string]any) string {
			if name, _ := vars["name"].(string); name == "first" {
				return labelLookupBody(false) // absent, and NOT to be created
			}
			return dropConnection
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("first") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "first,tools",
	})
	if err == nil {
		t.Fatalf("expected an error from the dropped lookup connection, got nil (ops: %v)", opsOf(*reqs))
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Errorf("err = %v, want the error to name the label", err)
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 2 {
		t.Errorf("filtered label lookups = %d, want 2 — the failure under test is the LOOKUP's, on the SECOND name (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 — a failed lookup is not an absent label (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueCreate"); got != 0 {
		t.Errorf("issueCreate sent %d time(s), want 0 — no ticket lands on a lookup failure (ops: %v)", got, opsOf(*reqs))
	}
}

// TestCreateTicket_LabelLookupGraphQLError_NamesLabelNoCreate — R2 arm (b).
func TestCreateTicket_LabelLookupGraphQLError_NamesLabelNoCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, vars map[string]any) string {
			if name, _ := vars["name"].(string); name == "first" {
				return labelLookupBody(false) // absent, and NOT to be created
			}
			return `{"data":null,"errors":[{"message":"Argument Validation Error"}]}`
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("first") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "first,tools",
	})
	if err == nil {
		t.Fatalf("expected an error from the lookup's GraphQL errors[], got nil (ops: %v)", opsOf(*reqs))
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Errorf("err = %v, want the error to name the label", err)
	}
	// ATTRIBUTION: the failure under test is the one the TRACKER returned, so
	// the lookup must have actually been issued. Without this the test stays
	// green if lookupLabel ever returns before sending its request.
	if got := countOp(*reqs, "TeamLabelByName"); got != 2 {
		t.Errorf("filtered label lookups = %d, want 2 — the failure is the tracker's reply to the SECOND name's request (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 — a failed lookup is not an absent label (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueCreate"); got != 0 {
		t.Errorf("issueCreate sent %d time(s), want 0 (ops: %v)", got, opsOf(*reqs))
	}
}

// TestCreateTicket_LabelLookupNullTeam_NamesLabelNoCreate — R2 arm (c), the
// adapter's own: a 200 response whose team is null. Nothing in the client's
// classification covers it, so the adapter must refuse rather than read the
// empty node list as "the label does not exist" and create a duplicate.
func TestCreateTicket_LabelLookupNullTeam_NamesLabelNoCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, vars map[string]any) string {
			if name, _ := vars["name"].(string); name == "first" {
				return labelLookupBody(false) // absent, and NOT to be created
			}
			return `{"data":{"team":null}}`
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("first") },
		"IssueCreate":      func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "first,tools",
	})
	if err == nil {
		t.Fatalf("expected an error from a null team on the lookup, got nil (ops: %v)", opsOf(*reqs))
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Errorf("err = %v, want the error to name the label", err)
	}
	// ATTRIBUTION: the null team is something the tracker REPLIED, so the
	// lookup must have been issued to hear it. Without this the test stays
	// green if lookupLabel ever returns before sending its request.
	if got := countOp(*reqs, "TeamLabelByName"); got != 2 {
		t.Errorf("filtered label lookups = %d, want 2 — a null team is a reply to the SECOND name's request (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueCreate"); got != 0 {
		t.Errorf("issueCreate sent %d time(s), want 0 (ops: %v)", got, opsOf(*reqs))
	}
}

// ---------------- R3: declared order reaches issueCreate ----------------

// TestCreateTicket_LabelIDs_InCallerDeclaredOrder — R3. Three labels
// resolved by three SEPARATE lookups must reach issueCreate in the caller's
// declared order, not lookup-completion order. The create path had no
// multi-label order coverage before this ticket. The list carries surrounding
// spaces, which ensureLabels trims.
func TestCreateTicket_LabelIDs_InCallerDeclaredOrder(t *testing.T) {
	ids := map[string]string{
		"tools":       "label_uuid_tools",
		"correctness": "label_uuid_correctness",
		"bug-hunt":    "label_uuid_bughunt",
	}
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, vars map[string]any) string {
			name, _ := vars["name"].(string)
			id, ok := ids[name]
			if !ok {
				return labelLookupBody(false)
			}
			return labelLookupBody(false,
				labelMatch{ID: id, Name: name, TeamID: "team_uuid_1", TeamKey: "ABC"})
		},
		"IssueCreate": func(int, map[string]any) string { return issueCreateBody() },
	})
	b := backendForServer(srv)
	if _, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Labels: "tools, correctness ,bug-hunt",
	}); err != nil {
		t.Fatalf("CreateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 3 {
		t.Errorf("filtered label lookups = %d, want 3 (one per declared name) (ops: %v)", got, opsOf(*reqs))
	}
	// The names as sent must be trimmed, and in declared order.
	var sent []string
	for _, r := range reqsFor(*reqs, "TeamLabelByName") {
		s, _ := r.Vars["name"].(string)
		sent = append(sent, s)
	}
	if strings.Join(sent, "|") != "tools|correctness|bug-hunt" {
		t.Errorf("lookup names as sent = %v, want [tools correctness bug-hunt] trimmed and in declared order", sent)
	}
	creates := reqsFor(*reqs, "IssueCreate")
	if len(creates) != 1 {
		t.Fatalf("issueCreate sent %d time(s), want 1 (ops: %v)", len(creates), opsOf(*reqs))
	}
	got := labelIDsOf(t, creates[0])
	want := []string{"label_uuid_tools", "label_uuid_correctness", "label_uuid_bughunt"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("issueCreate input.labelIds = %v, want %v in the caller's declared order", got, want)
	}
}

// ---------------- the update paths get the same treatment ----------------

// TestUpdateTicket_LabelOneMatch_ReusedWithoutCreate — ensureLabels serves
// all four callers off the team id it already holds, so the update path must
// show the same reuse.
func TestUpdateTicket_LabelOneMatch_ReusedWithoutCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"IssueByID": func(int, map[string]any) string {
			return `{"data":{"issue":{"id":"issue_uuid_1","team":{"id":"team_uuid_1","key":"ABC"}}}}`
		},
		"TeamByID": func(int, map[string]any) string { return teamByIDBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_tools", Name: "tools", TeamID: "team_uuid_1", TeamKey: "ABC"})
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("tools") },
		"IssueUpdate": func(int, map[string]any) string {
			return `{"data":{"issueUpdate":{"issue":{"id":"issue_uuid_1","state":{"name":"Todo"}}}}}`
		},
	})
	b := backendForServer(srv)
	if err := b.UpdateTicket(context.Background(),
		backends.RemoteRef{ID: "issue_uuid_1"},
		backends.TicketDiff{Labels: new("TOOLS")}); err != nil {
		t.Fatalf("UpdateTicket: %v (ops: %v)", err, opsOf(*reqs))
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 (ops: %v)", got, opsOf(*reqs))
	}
	// The UPDATE path resolves its team through teamByIDQuery, a different
	// constant from the create path's teamByKeyQuery. R1's no-bulk-read clause
	// reaches both, and the create-path assertion is blind to this one.
	assertNoBulkLabelRead(t, *reqs, "TeamByID")
	updates := reqsFor(*reqs, "IssueUpdate")
	if len(updates) != 1 {
		t.Fatalf("issueUpdate sent %d time(s), want 1 (ops: %v)", len(updates), opsOf(*reqs))
	}
	if got := labelIDsOf(t, updates[0]); len(got) != 1 || got[0] != "label_uuid_tools" {
		t.Errorf("issueUpdate input.labelIds = %v, want [label_uuid_tools]", got)
	}
}

// TestUpdateProject_LabelOneMatch_ReusedWithoutCreate — the fourth caller,
// reached through resolveProjectLabels.
func TestUpdateProject_LabelOneMatch_ReusedWithoutCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"ProjectByID": func(int, map[string]any) string {
			return `{"data":{"project":{"id":"proj_uuid_1","teams":{"nodes":[{"id":"team_uuid_1","key":"ABC"}]}}}}`
		},
		"TeamByID": func(int, map[string]any) string { return teamByIDBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_docs", Name: "docs", TeamID: "team_uuid_1", TeamKey: "ABC"})
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("docs") },
		"ProjectUpdate": func(int, map[string]any) string {
			return `{"data":{"projectUpdate":{"project":{"id":"proj_uuid_1","state":"started"}}}}`
		},
	})
	b := backendForServer(srv)
	if err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Labels: new("Docs")}); err != nil {
		t.Fatalf("UpdateProject: %v (ops: %v)", err, opsOf(*reqs))
	}
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 (ops: %v)", got, opsOf(*reqs))
	}
	// Same constant as the ticket update path (teamByIDQuery), reached through
	// resolveProjectLabels; asserted here too so neither caller can regress
	// alone.
	assertNoBulkLabelRead(t, *reqs, "TeamByID")
	updates := reqsFor(*reqs, "ProjectUpdate")
	if len(updates) != 1 {
		t.Fatalf("projectUpdate sent %d time(s), want 1 (ops: %v)", len(updates), opsOf(*reqs))
	}
	if got := labelIDsOf(t, updates[0]); len(got) != 1 || got[0] != "label_uuid_docs" {
		t.Errorf("projectUpdate input.labelIds = %v, want [label_uuid_docs]", got)
	}
}

// TestEnsureLabels_EmptyList_IssuesNoRequest — the empty-list return-fast
// (backend_write.go's FIRST-line contract) must stay ahead of the lookup, or
// every label-free write would pay a request it does not need.
func TestEnsureLabels_EmptyList_IssuesNoRequest(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		// The control's lookup HITS, so the control records exactly one request
		// and needs no create. Scripting a miss instead would send the control
		// on to issueLabelCreate, which this test has no reason to script and
		// whose absence the fake reports as a harness failure.
		"TeamLabelByName": func(int, map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_tools", Name: "tools", TeamID: fixtureTeamID, TeamKey: fixtureTeamKey})
		},
	})
	b := backendForServer(srv)
	team := &resolvedTeam{ID: fixtureTeamID, Key: fixtureTeamKey}
	// CONTROL: a list that DOES name a label records a request, so the zero
	// asserted below is a real absence and not a recorder that never appends.
	if _, err := b.ensureLabels(context.Background(), team, "tools"); err != nil {
		t.Fatalf("control setup: ensureLabels(\"tools\"): %v", err)
	}
	before := len(*reqs)
	if before == 0 {
		t.Fatalf("control failed: a named label recorded no request, so this test cannot observe an absence")
	}
	// SUBJECT: the empty list must add nothing to that count.
	got, err := b.ensureLabels(context.Background(), team, "")
	if err != nil {
		t.Fatalf("ensureLabels: %v", err)
	}
	if got != nil {
		t.Errorf("result = %v, want nil", got)
	}
	if len(*reqs) != before {
		t.Errorf("requests went %d -> %d; an empty label list must issue no lookup (ops: %v)", before, len(*reqs), opsOf(*reqs))
	}
}

// TestEnsureLabels_OnlySeparators_IssuesNoRequest — a list of nothing but
// separators and spaces trims to zero names, so it must issue no lookup
// either. ensureLabels skips empty names inside the loop.
func TestEnsureLabels_OnlySeparators_IssuesNoRequest(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		// The control's lookup HITS, so the control records exactly one request
		// and needs no create. Scripting a miss instead would send the control
		// on to issueLabelCreate, which this test has no reason to script and
		// whose absence the fake reports as a harness failure.
		"TeamLabelByName": func(int, map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_tools", Name: "tools", TeamID: fixtureTeamID, TeamKey: fixtureTeamKey})
		},
	})
	b := backendForServer(srv)
	team := &resolvedTeam{ID: fixtureTeamID, Key: fixtureTeamKey}
	// CONTROL: a list that DOES name a label records a request, so the zero
	// asserted below is a real absence and not a recorder that never appends.
	if _, err := b.ensureLabels(context.Background(), team, "tools"); err != nil {
		t.Fatalf("control setup: ensureLabels(\"tools\"): %v", err)
	}
	before := len(*reqs)
	if before == 0 {
		t.Fatalf("control failed: a named label recorded no request, so this test cannot observe an absence")
	}
	// SUBJECT: a list of nothing but separators must add nothing to that count.
	got, err := b.ensureLabels(context.Background(), team, " , , ")
	if err != nil {
		t.Fatalf("ensureLabels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("result = %v, want empty", got)
	}
	if len(*reqs) != before {
		t.Errorf("requests went %d -> %d; a separators-only list names no label (ops: %v)", before, len(*reqs), opsOf(*reqs))
	}
}
