// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// ---------------- CreateTicket ----------------

func TestCreateTicket_Success(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		// teamByKey: states ["Todo","In Review","Done"]; labels [] —
		// teams(filter:{key:{eq:"ABC"}}) shape (single-element nodes[]).
		`{"data":{"teams":{"nodes":[{"id":"team_uuid_1","key":"ABC",
		  "states":{"nodes":[
		    {"id":"state_uuid_1","name":"Todo"},
		    {"id":"state_uuid_2","name":"In Review"},
		    {"id":"state_uuid_3","name":"Done"}
		  ]},
		  "labels":{"nodes":[]}
		}]}}}`,
		// issueCreate
		`{"data":{"issueCreate":{"issue":{"id":"issue_uuid_1","identifier":"ABC-1","title":"T","url":"http://l/i1","state":{"name":"In Review"}}}}}`,
	})
	b := backendForServer(srv)
	ref, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey:   "ABC",
		ProjectRef: backends.RemoteRef{ID: "proj_uuid_1"},
		Name:       "T",
		Status:     "In Review",
		Priority:   2,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if ref.ID != "issue_uuid_1" || ref.Identifier != "ABC-1" || ref.URL != "http://l/i1" || ref.State != "In Review" {
		t.Errorf("ref = %+v, want {issue_uuid_1 ABC-1 http://l/i1 In Review}", ref)
	}
	// 2 calls: teamByKey, issueCreate
	if len(*calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(*calls))
	}
	var req struct {
		Variables struct {
			Input map[string]any `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[1], &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := req.Variables.Input["stateId"]; got != "state_uuid_2" {
		t.Errorf("input.stateId = %v, want state_uuid_2 (resolved from \"In Review\")", got)
	}
	if got := req.Variables.Input["projectId"]; got != "proj_uuid_1" {
		t.Errorf("input.projectId = %v, want proj_uuid_1", got)
	}
	if got := req.Variables.Input["teamId"]; got != "team_uuid_1" {
		t.Errorf("input.teamId = %v, want team_uuid_1", got)
	}
}

func TestCreateTicket_GroupNotFound(t *testing.T) {
	srv, _ := scriptedServer(t, []string{
		// teams(filter:{key:{eq:"NOPE"}}) returns no matches.
		`{"data":{"teams":{"nodes":[]}}}`,
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "NOPE", Name: "T",
	})
	var gnf *ErrGroupNotFound
	if !errors.As(err, &gnf) {
		t.Fatalf("err = %v, want *ErrGroupNotFound", err)
	}
	if gnf.GroupKey != "NOPE" {
		t.Errorf("GroupKey = %q, want NOPE", gnf.GroupKey)
	}
}

// TestCreateTicket_UnknownStatus — issue state IS team-scoped; the
// ErrUnknownState carries non-empty GroupKey. Distinguishes the issue
// path from the project path.
func TestCreateTicket_UnknownStatus(t *testing.T) {
	srv, _ := scriptedServer(t, []string{
		// teamByKey returns a team WITHOUT the requested state — wrapped in
		// the teams(filter:){nodes[]} envelope.
		`{"data":{"teams":{"nodes":[{"id":"team_uuid_1","key":"ABC",
		  "states":{"nodes":[{"id":"state_uuid_1","name":"Todo"}]},
		  "labels":{"nodes":[]}
		}]}}}`,
	})
	b := backendForServer(srv)
	_, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: "ABC", Name: "T", Status: "NotAState",
	})
	var u *ErrUnknownState
	if !errors.As(err, &u) {
		t.Fatalf("err = %v, want *ErrUnknownState", err)
	}
	if u.GroupKey != "ABC" {
		t.Errorf("GroupKey = %q, want ABC (issue state IS team-scoped)", u.GroupKey)
	}
	if u.State != "NotAState" {
		t.Errorf("State = %q, want NotAState", u.State)
	}
}

// ---------------- UpdateTicket ----------------

func TestUpdateTicket_StatusOnly(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		// issueByID
		`{"data":{"issue":{"id":"issue_uuid_1","team":{"id":"team_uuid_1","key":"ABC"}}}}`,
		// teamByID
		`{"data":{"team":{"id":"team_uuid_1","key":"ABC",
		  "states":{"nodes":[{"id":"state_uuid_2","name":"In Review"}]},
		  "labels":{"nodes":[]}
		}}}`,
		// issueUpdate
		`{"data":{"issueUpdate":{"issue":{"id":"issue_uuid_1","state":{"name":"In Review"}}}}}`,
	})
	b := backendForServer(srv)
	err := b.UpdateTicket(context.Background(),
		backends.RemoteRef{ID: "issue_uuid_1"},
		backends.TicketDiff{Status: new("In Review")})
	if err != nil {
		t.Fatalf("UpdateTicket: %v", err)
	}
	if len(*calls) != 3 {
		t.Fatalf("calls = %d, want 3 (issueByID, teamByID, issueUpdate)", len(*calls))
	}
	// Inspect the issueUpdate input.
	var req struct {
		Variables struct {
			ID    string         `json:"id"`
			Input map[string]any `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[2], &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Variables.ID != "issue_uuid_1" {
		t.Errorf("variables.id = %q, want issue_uuid_1", req.Variables.ID)
	}
	if got := req.Variables.Input["stateId"]; got != "state_uuid_2" {
		t.Errorf("input.stateId = %v, want state_uuid_2", got)
	}
	if _, has := req.Variables.Input["title"]; has {
		t.Errorf("input should not have title for status-only diff")
	}
}

// TestUpdateTicket_NoLookupOnNonStatusDiff — name/description/priority
// only diff must NOT trigger issueByID/teamByID lookups.
func TestUpdateTicket_NoLookupOnNonStatusDiff(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		`{"data":{"issueUpdate":{"issue":{"id":"issue_uuid_1","state":{"name":"Todo"}}}}}`,
	})
	b := backendForServer(srv)
	err := b.UpdateTicket(context.Background(),
		backends.RemoteRef{ID: "issue_uuid_1"},
		backends.TicketDiff{Description: new("new desc"), Priority: new(3)})
	if err != nil {
		t.Fatalf("UpdateTicket: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d, want 1 (no team lookup needed for desc/priority)", len(*calls))
	}
}

// TestUpdateTicket_LabelCreateOnTheFly_Success — UpdateTicket with a
// new label exercises the team-resolution → ensureLabels →
// issueLabelCreate → issueUpdate happy path.
func TestUpdateTicket_LabelCreateOnTheFly_Success(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		// issueByID
		`{"data":{"issue":{"id":"issue_uuid_1","team":{"id":"team_uuid_1","key":"ABC"}}}}`,
		// teamByID — only "bug" exists; "new-label" must be created
		`{"data":{"team":{"id":"team_uuid_1","key":"ABC",
		  "states":{"nodes":[]},
		  "labels":{"nodes":[{"id":"label_uuid_bug","name":"bug"}]}
		}}}`,
		// issueLabelCreate succeeds
		`{"data":{"issueLabelCreate":{"issueLabel":{"id":"label_uuid_new","name":"new-label"}}}}`,
		// issueUpdate
		`{"data":{"issueUpdate":{"issue":{"id":"issue_uuid_1","state":{"name":"Todo"}}}}}`,
	})
	b := backendForServer(srv)
	err := b.UpdateTicket(context.Background(),
		backends.RemoteRef{ID: "issue_uuid_1"},
		backends.TicketDiff{Labels: new("bug,new-label")})
	if err != nil {
		t.Fatalf("UpdateTicket: %v", err)
	}
	if len(*calls) != 4 {
		t.Fatalf("calls = %d, want 4 (issueByID, teamByID, issueLabelCreate, issueUpdate)", len(*calls))
	}
	// Verify issueLabelCreate input.
	var lc struct {
		Variables struct {
			Input struct {
				Name   string `json:"name"`
				TeamID string `json:"teamId"`
			} `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[2], &lc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if lc.Variables.Input.Name != "new-label" {
		t.Errorf("issueLabelCreate input.name = %q, want new-label", lc.Variables.Input.Name)
	}
	if lc.Variables.Input.TeamID != "team_uuid_1" {
		t.Errorf("issueLabelCreate input.teamId = %q, want team_uuid_1", lc.Variables.Input.TeamID)
	}
	// Verify issueUpdate input has both label UUIDs.
	var iu struct {
		Variables struct {
			Input map[string]any `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[3], &iu); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	labelIDs, ok := iu.Variables.Input["labelIds"].([]any)
	if !ok || len(labelIDs) != 2 {
		t.Fatalf("issueUpdate input.labelIds = %v, want [label_uuid_bug label_uuid_new]", iu.Variables.Input["labelIds"])
	}
	if labelIDs[0] != "label_uuid_bug" || labelIDs[1] != "label_uuid_new" {
		t.Errorf("labelIds = %v, want [label_uuid_bug label_uuid_new] in declaration order", labelIDs)
	}
}

// TestUpdateTicket_LabelCreateOnTheFly_HardErrorOnCreate — issueLabelCreate
// fails; ensureLabels HARD-ERRORS and UpdateTicket returns wrapped error
// (no issueUpdate call).
func TestUpdateTicket_LabelCreateOnTheFly_HardErrorOnCreate(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		// issueByID
		`{"data":{"issue":{"id":"issue_uuid_1","team":{"id":"team_uuid_1","key":"ABC"}}}}`,
		// teamByID — no labels
		`{"data":{"team":{"id":"team_uuid_1","key":"ABC",
		  "states":{"nodes":[]},
		  "labels":{"nodes":[]}
		}}}`,
		// issueLabelCreate fails
		`{"data":null,"errors":[{"message":"label create rejected"}]}`,
	})
	b := backendForServer(srv)
	err := b.UpdateTicket(context.Background(),
		backends.RemoteRef{ID: "issue_uuid_1"},
		backends.TicketDiff{Labels: new("brand-new-label")})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "brand-new-label") {
		t.Errorf("err = %v, want wrapped error mentioning the label name", err)
	}
	// HARD-ERROR contract: NO issueUpdate call after the create failure.
	if len(*calls) != 3 {
		t.Errorf("calls = %d, want 3 (issueByID, teamByID, issueLabelCreate; NO issueUpdate)", len(*calls))
	}
}

// ---------------- ArchiveTicket ----------------

func TestArchiveTicket(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		`{"data":{"issueArchive":{"success":true}}}`,
	})
	b := backendForServer(srv)
	if err := b.ArchiveTicket(context.Background(), backends.RemoteRef{ID: "issue_uuid_1"}); err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(*calls))
	}
	var req struct {
		Variables struct {
			ID string `json:"id"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[0], &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Variables.ID != "issue_uuid_1" {
		t.Errorf("variables.id = %q, want issue_uuid_1", req.Variables.ID)
	}
}
