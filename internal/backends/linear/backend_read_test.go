// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// scriptedServer returns successive bodies from `responses` on each call,
// recording the request body each time. Mirrors the captured/newFakeServer
// shape from client_test.go but for ordered multi-call scripts (pagination).
// If the test makes more requests than scripted responses, the last response
// is reused — keeps the SyncGroup pagination tests honest about extra calls
// rather than panicking on index-out-of-range.
//
// Generic placeholders only — never real Linear team/workspace identifiers
// in fixtures.
func scriptedServer(t *testing.T, responses []string) (*httptest.Server, *[]json.RawMessage) {
	t.Helper()
	calls := make([]json.RawMessage, 0, len(responses))
	var i int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, json.RawMessage(body))
		idx := i
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responses[idx]))
		i++
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// backendForServer wires a Backend to a scripted httptest.Server. The
// APIKey is the generic placeholder lin_api_test — never a real key.
func backendForServer(srv *httptest.Server) *Backend {
	return &Backend{Client: &Client{APIKey: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}}
}

// teamsResponseABC is the canonical first-call response for SyncGroup tests
// that target group "ABC". Linear's Query.team accepts only `id`, so
// SyncGroup resolves key→id by listing teams via Groups() and matching
// client-side. Tests prepend this fixture so the resolution succeeds; the
// returned UUID is "team_1", matching the team(id:) responses that follow.
const teamsResponseABC = `{"data":{"teams":{"nodes":[
  {"id":"team_1","key":"ABC","name":"Alpha"}
]}}}`

func TestGroups_MapsTeamsToGroups(t *testing.T) {
	srv, _ := scriptedServer(t, []string{
		`{"data":{"teams":{"nodes":[
		  {"id":"team_1","key":"ABC","name":"Alpha"},
		  {"id":"team_2","key":"XYZ","name":"Xray"}
		]}}}`,
	})
	b := backendForServer(srv)
	groups, err := b.Groups(context.Background())
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2", len(groups))
	}
	if groups[0].ID != "team_1" || groups[0].Key != "ABC" || groups[0].Name != "Alpha" {
		t.Errorf("groups[0] = %+v, want {team_1 ABC Alpha}", groups[0])
	}
	if groups[1].ID != "team_2" || groups[1].Key != "XYZ" || groups[1].Name != "Xray" {
		t.Errorf("groups[1] = %+v, want {team_2 XYZ Xray}", groups[1])
	}
}

func TestSyncGroup_PaginatesProjectsAndIssues(t *testing.T) {
	// Four-call script: project page 1 (hasNextPage=true), project page 2
	// (hasNextPage=false), issue page 1 (hasNextPage=true), issue page 2
	// (hasNextPage=false). Assert all 4 projects + 4 tickets accumulate
	// AND the second project request carried `after` = first endCursor.
	const projCursor = "proj_cursor_1"
	const issueCursor = "issue_cursor_1"
	srv, calls := scriptedServer(t, []string{
		// teams() — SyncGroup's first call resolves key→id.
		teamsResponseABC,
		// projects page 1
		`{"data":{"team":{"id":"team_1","key":"ABC","name":"Alpha","projects":{
		  "pageInfo":{"hasNextPage":true,"endCursor":"` + projCursor + `"},
		  "nodes":[
		    {"id":"proj_uuid_1","name":"P1","description":"","url":"http://l/p1","state":"started","priority":0,"labels":{"nodes":[]},"archivedAt":null},
		    {"id":"proj_uuid_2","name":"P2","description":"","url":"http://l/p2","state":"backlog","priority":0,"labels":{"nodes":[]},"archivedAt":null}
		  ]
		}}}}`,
		// projects page 2
		`{"data":{"team":{"id":"team_1","key":"ABC","name":"Alpha","projects":{
		  "pageInfo":{"hasNextPage":false,"endCursor":""},
		  "nodes":[
		    {"id":"proj_uuid_3","name":"P3","description":"","url":"http://l/p3","state":"completed","priority":0,"labels":{"nodes":[]},"archivedAt":null},
		    {"id":"proj_uuid_4","name":"P4","description":"","url":"http://l/p4","state":"planned","priority":0,"labels":{"nodes":[]},"archivedAt":null}
		  ]
		}}}}`,
		// issues page 1
		`{"data":{"team":{"issues":{
		  "pageInfo":{"hasNextPage":true,"endCursor":"` + issueCursor + `"},
		  "nodes":[
		    {"id":"issue_uuid_1","identifier":"ABC-1","title":"T1","description":"","url":"http://l/i1","state":{"name":"Todo"},"priority":0,"labels":{"nodes":[]},"project":null,"archivedAt":null},
		    {"id":"issue_uuid_2","identifier":"ABC-2","title":"T2","description":"","url":"http://l/i2","state":{"name":"Todo"},"priority":0,"labels":{"nodes":[]},"project":null,"archivedAt":null}
		  ]
		}}}}`,
		// issues page 2
		`{"data":{"team":{"issues":{
		  "pageInfo":{"hasNextPage":false,"endCursor":""},
		  "nodes":[
		    {"id":"issue_uuid_3","identifier":"ABC-3","title":"T3","description":"","url":"http://l/i3","state":{"name":"Todo"},"priority":0,"labels":{"nodes":[]},"project":null,"archivedAt":null},
		    {"id":"issue_uuid_4","identifier":"ABC-4","title":"T4","description":"","url":"http://l/i4","state":{"name":"Done"},"priority":0,"labels":{"nodes":[]},"project":null,"archivedAt":null}
		  ]
		}}}}`,
	})
	b := backendForServer(srv)
	snap, err := b.SyncGroup(context.Background(), "ABC")
	if err != nil {
		t.Fatalf("SyncGroup: %v", err)
	}
	if len(snap.Projects) != 4 {
		t.Errorf("len(snap.Projects) = %d, want 4", len(snap.Projects))
	}
	if len(snap.Tickets) != 4 {
		t.Errorf("len(snap.Tickets) = %d, want 4", len(snap.Tickets))
	}
	if len(*calls) != 5 {
		t.Fatalf("server saw %d calls, want 5 (teams + 2 project pages + 2 issue pages)", len(*calls))
	}

	// Pagination contract: 3rd request (2nd projects page) must carry
	// `after` = projCursor from the 1st projects response. The teams()
	// call is index 0; the 1st projects page is index 1; the 2nd is
	// index 2. The variable name in the projects/issues queries is
	// `teamID` (Linear's Query.team takes id only).
	var second struct {
		Variables struct {
			TeamID string `json:"teamID"`
			After  string `json:"after"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[2], &second); err != nil {
		t.Fatalf("unmarshal call[2]: %v", err)
	}
	if second.Variables.After != projCursor {
		t.Errorf("call[2].variables.after = %q, want %q (project endCursor)",
			second.Variables.After, projCursor)
	}
	if second.Variables.TeamID != "team_1" {
		t.Errorf("call[2].variables.teamID = %q, want team_1", second.Variables.TeamID)
	}

	// 5th request (2nd issues page) must carry `after` = issueCursor.
	var fifth struct {
		Variables struct {
			After string `json:"after"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[4], &fifth); err != nil {
		t.Fatalf("unmarshal call[4]: %v", err)
	}
	if fifth.Variables.After != issueCursor {
		t.Errorf("call[4].variables.after = %q, want %q (issue endCursor)",
			fifth.Variables.After, issueCursor)
	}

	// First projects page (call[1]) must NOT carry `after` (cold start).
	if strings.Contains(string((*calls)[1]), `"after":`) {
		t.Errorf("call[1] should not carry `after` on cold-start: %s", string((*calls)[1]))
	}
}

func TestSyncGroup_GroupNotFound(t *testing.T) {
	// teams() returns workspace teams that do NOT include "NOPE"; SyncGroup's
	// key→id resolution surfaces *ErrGroupNotFound before any team(id:) call.
	srv, _ := scriptedServer(t, []string{
		`{"data":{"teams":{"nodes":[{"id":"team_1","key":"ABC","name":"Alpha"}]}}}`,
	})
	b := backendForServer(srv)
	_, err := b.SyncGroup(context.Background(), "NOPE")
	if err == nil {
		t.Fatalf("SyncGroup(NOPE) returned nil error, want *ErrGroupNotFound")
	}
	var gnf *ErrGroupNotFound
	if !errors.As(err, &gnf) {
		t.Fatalf("err = %v, want errors.As *ErrGroupNotFound", err)
	}
	if gnf.GroupKey != "NOPE" {
		t.Errorf("GroupKey = %q, want NOPE", gnf.GroupKey)
	}
	// T4 Phase 0 retrofit: typed wrap surfaces ReasonNotFound terminal.
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if be.Reason != backends.ReasonNotFound || be.Transient {
		t.Errorf("typed wrap misclassified: Reason=%q Transient=%v, want %q/false",
			be.Reason, be.Transient, backends.ReasonNotFound)
	}
}

func TestSyncGroup_IncludesArchived(t *testing.T) {
	// One archived project (archivedAt non-null), one live; assert both
	// appear in snapshot AND archived bit is correctly mapped.
	srv, _ := scriptedServer(t, []string{
		teamsResponseABC,
		`{"data":{"team":{"id":"team_1","key":"ABC","name":"Alpha","projects":{
		  "pageInfo":{"hasNextPage":false,"endCursor":""},
		  "nodes":[
		    {"id":"proj_uuid_1","name":"Live","description":"","url":"http://l/p1","state":"started","priority":0,"labels":{"nodes":[]},"archivedAt":null},
		    {"id":"proj_uuid_2","name":"Old","description":"","url":"http://l/p2","state":"completed","priority":0,"labels":{"nodes":[]},"archivedAt":"2024-01-01T00:00:00Z"}
		  ]
		}}}}`,
		`{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`,
	})
	b := backendForServer(srv)
	snap, err := b.SyncGroup(context.Background(), "ABC")
	if err != nil {
		t.Fatalf("SyncGroup: %v", err)
	}
	if len(snap.Projects) != 2 {
		t.Fatalf("len(snap.Projects) = %d, want 2", len(snap.Projects))
	}
	if snap.Projects[0].Archived {
		t.Errorf("snap.Projects[0].Archived = true, want false (archivedAt null)")
	}
	if !snap.Projects[1].Archived {
		t.Errorf("snap.Projects[1].Archived = false, want true (archivedAt non-null)")
	}
}

func TestSyncGroup_LabelsCommaJoined(t *testing.T) {
	srv, _ := scriptedServer(t, []string{
		teamsResponseABC,
		`{"data":{"team":{"id":"team_1","key":"ABC","name":"Alpha","projects":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`,
		`{"data":{"team":{"issues":{
		  "pageInfo":{"hasNextPage":false,"endCursor":""},
		  "nodes":[
		    {"id":"issue_uuid_1","identifier":"ABC-1","title":"T1","description":"","url":"http://l/i1","state":{"name":"Todo"},"priority":0,
		     "labels":{"nodes":[{"name":"bug"},{"name":"backend"}]},
		     "project":null,"archivedAt":null}
		  ]
		}}}}`,
	})
	b := backendForServer(srv)
	snap, err := b.SyncGroup(context.Background(), "ABC")
	if err != nil {
		t.Fatalf("SyncGroup: %v", err)
	}
	if len(snap.Tickets) != 1 {
		t.Fatalf("len(snap.Tickets) = %d, want 1", len(snap.Tickets))
	}
	if got := snap.Tickets[0].Labels; got != "bug,backend" {
		t.Errorf("Labels = %q, want %q", got, "bug,backend")
	}
}

func TestSyncGroup_StatusVerbatim(t *testing.T) {
	// Linear-shaped state name "In Review" must round-trip verbatim — no
	// normalization to "in_review" / "in-review" / lowercase. Asserts the
	// status passthrough contract from backends/backend.go package doc.
	srv, _ := scriptedServer(t, []string{
		teamsResponseABC,
		`{"data":{"team":{"id":"team_1","key":"ABC","name":"Alpha","projects":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`,
		`{"data":{"team":{"issues":{
		  "pageInfo":{"hasNextPage":false,"endCursor":""},
		  "nodes":[
		    {"id":"issue_uuid_1","identifier":"ABC-1","title":"T1","description":"","url":"http://l/i1","state":{"name":"In Review"},"priority":0,"labels":{"nodes":[]},"project":null,"archivedAt":null}
		  ]
		}}}}`,
	})
	b := backendForServer(srv)
	snap, err := b.SyncGroup(context.Background(), "ABC")
	if err != nil {
		t.Fatalf("SyncGroup: %v", err)
	}
	if len(snap.Tickets) != 1 {
		t.Fatalf("len(snap.Tickets) = %d, want 1", len(snap.Tickets))
	}
	if got := snap.Tickets[0].Status; got != "In Review" {
		t.Errorf("Status = %q, want %q (verbatim, no normalization)", got, "In Review")
	}
}

// TestBackendName_Literal pins the cross-phase contract: Name() returns
// the literal string "linear". T2 stores this on local nodes; T3 routes
// per-node updates by it. Changing the string breaks both.
func TestBackendName_Literal(t *testing.T) {
	b := &Backend{}
	if got := b.Name(); got != "linear" {
		t.Errorf("Name() = %q, want %q", got, "linear")
	}
}

// TestSyncGroup_FieldMapping_DescriptionToSummary_ContentToDescription locks
// in the read-side asymmetric mapping that mirrors the write path:
//
//	Linear.description (short tagline) → Snapshot.Summary
//	Linear.content     (long body)     → Snapshot.Description
//
// Without this test the mapping could regress to "description→Description"
// silently — the round-trip would still "work" with empty content, but real
// projects would lose the body field on every pull.
func TestSyncGroup_FieldMapping_DescriptionToSummary_ContentToDescription(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		teamsResponseABC,
		`{"data":{"team":{"id":"team_1","key":"ABC","name":"Alpha","projects":{
		  "pageInfo":{"hasNextPage":false,"endCursor":""},
		  "nodes":[
		    {"id":"proj_uuid_1","name":"P1",
		     "description":"the short tagline",
		     "content":"# Long body\nWith markdown.\n\nMultiple paragraphs.",
		     "url":"http://l/p1","state":"started","priority":0,
		     "labels":{"nodes":[]},"archivedAt":null}
		  ]
		}}}}`,
		`{"data":{"team":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}`,
	})
	b := backendForServer(srv)
	snap, err := b.SyncGroup(context.Background(), "ABC")
	if err != nil {
		t.Fatalf("SyncGroup: %v", err)
	}
	if len(snap.Projects) != 1 {
		t.Fatalf("len(snap.Projects) = %d, want 1", len(snap.Projects))
	}
	p := snap.Projects[0]
	if p.Summary != "the short tagline" {
		t.Errorf("Summary = %q, want %q (Linear.description should map to Snapshot.Summary)",
			p.Summary, "the short tagline")
	}
	if p.Description != "# Long body\nWith markdown.\n\nMultiple paragraphs." {
		t.Errorf("Description = %q, want long markdown body (Linear.content should map to Snapshot.Description)",
			p.Description)
	}

	// Sanity: the GraphQL query string must request `content` — without it
	// Linear returns an empty content field and the mapping looks empty
	// even though the server has data.
	if !strings.Contains(string((*calls)[1]), "content") {
		t.Errorf("teamProjectsQuery did not request `content`: %s", string((*calls)[1]))
	}
}
