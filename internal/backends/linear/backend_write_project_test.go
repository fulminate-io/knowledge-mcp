// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// intPtr builds *int for diff fields. Local convenience.
func intPtr(i int) *int { return new(i) }

// ---------------- CreateProject ----------------
// TestCreateProject_Success — rescripted onto opServer: the label "bug" now
// resolves through a filtered per-name lookup between the team read and the
// project create, which the positional scriptedServer cannot express.
func TestCreateProject_Success(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		// Linear's Query.team takes id only, so resolveTeamByKey uses
		// teams(filter:{key:{eq:...}}); the fixture matches that shape.
		"TeamByKey": func(int, map[string]any) string {
			return `{"data":{"teams":{"nodes":[{"id":"team_uuid_1","key":"ABC",
			  "states":{"nodes":[]}
			}]}}}`
		},
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_bug", Name: "bug", TeamID: "team_uuid_1", TeamKey: "ABC"})
		},
		"IssueLabelCreate": func(_ int, _ map[string]any) string { return issueLabelCreateBody("bug") },
		"ProjectCreate": func(int, map[string]any) string {
			return `{"data":{"projectCreate":{"project":{"id":"proj_uuid_1","name":"P","url":"http://l/p1","state":"started"}}}}`
		},
	})
	b := backendForServer(srv)
	ref, err := b.CreateProject(context.Background(), backends.ProjectCreateArgs{
		GroupKey:    "ABC",
		Name:        "P",
		Summary:     "tagline",
		Description: "long markdown body",
		Status:      "started",
		Priority:    1,
		Labels:      "bug",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if ref.ID != "proj_uuid_1" || ref.URL != "http://l/p1" || ref.State != "started" {
		t.Errorf("ref = %+v, want {ID:proj_uuid_1 URL:http://l/p1 State:started}", ref)
	}
	// teamByKey, one filtered label lookup, projectCreate — and NO
	// issueLabelCreate, because the team already holds "bug".
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 0 {
		t.Errorf("issueLabelCreate sent %d time(s), want 0 — the team already holds \"bug\" (ops: %v)", got, opsOf(*reqs))
	}
	creates := reqsFor(*reqs, "ProjectCreate")
	if len(creates) != 1 {
		t.Fatalf("projectCreate sent %d time(s), want 1 (ops: %v)", len(creates), opsOf(*reqs))
	}
	req := struct {
		Variables struct {
			Input map[string]any `json:"input"`
		}
	}{}
	req.Variables.Input, _ = creates[0].Vars["input"].(map[string]any)
	if got := req.Variables.Input["state"]; got != "started" {
		t.Errorf("input.state = %v, want %q (workspace-level enum string verbatim)", got, "started")
	}
	teamIDs, ok := req.Variables.Input["teamIds"].([]any)
	if !ok || len(teamIDs) != 1 || teamIDs[0] != "team_uuid_1" {
		t.Errorf("input.teamIds = %v, want [team_uuid_1]", req.Variables.Input["teamIds"])
	}
	labelIDs, ok := req.Variables.Input["labelIds"].([]any)
	if !ok || len(labelIDs) != 1 || labelIDs[0] != "label_uuid_bug" {
		t.Errorf("input.labelIds = %v, want [label_uuid_bug]", req.Variables.Input["labelIds"])
	}
	// THE NAME REACHES THE TRACKER VERBATIM. The client-side 80-rune project
	// name cap counts the RAW name precisely because nothing between the
	// create_project guard and this field trims it. That chain crosses two
	// packages; the tools half asserts the intercept passes the name through,
	// and this is the linear half — a trim introduced here would make the
	// client-side cap wrong with every test on the other side still green.
	if got := req.Variables.Input["name"]; got != "P" {
		t.Errorf("input.name = %v, want %q verbatim — nothing may trim or rewrite the caller's name", got, "P")
	}
	// Summary → description (Linear's short tagline, ≤255 chars).
	if got := req.Variables.Input["description"]; got != "tagline" {
		t.Errorf("input.description = %v, want %q (mapped from args.Summary)", got, "tagline")
	}
	// Description → content (Linear's long markdown body).
	if got := req.Variables.Input["content"]; got != "long markdown body" {
		t.Errorf("input.content = %v, want %q (mapped from args.Description)", got, "long markdown body")
	}
}

// TestCreateProject_OmitsEmptyTaglineAndBody asserts the empty-field
// omission contract: when Summary or Description is empty, the
// corresponding Linear field must not appear in the input map at all
// (rather than being sent as "").
func TestCreateProject_OmitsEmptyTaglineAndBody(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		`{"data":{"teams":{"nodes":[{"id":"team_uuid_1","key":"ABC",
		  "states":{"nodes":[]}
		}]}}}`,
		`{"data":{"projectCreate":{"project":{"id":"proj_uuid_2","name":"P2","url":"http://l/p2","state":""}}}}`,
	})
	b := backendForServer(srv)
	_, err := b.CreateProject(context.Background(), backends.ProjectCreateArgs{
		GroupKey: "ABC",
		Name:     "P2",
		// Summary + Description deliberately empty.
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	var req struct {
		Variables struct {
			Input map[string]any `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[1], &req); err != nil {
		t.Fatalf("unmarshal call[1]: %v", err)
	}
	if _, ok := req.Variables.Input["description"]; ok {
		t.Errorf("input.description was set; expected omitted when args.Summary is empty")
	}
	if _, ok := req.Variables.Input["content"]; ok {
		t.Errorf("input.content was set; expected omitted when args.Description is empty")
	}
}

// createProjectFixture is the 2-call scriptedServer shape (teamByKey +
// projectCreate) shared by the overflow tests below.
func createProjectFixture(t *testing.T) (*httptest.Server, *[]json.RawMessage) {
	t.Helper()
	return scriptedServer(t, []string{
		`{"data":{"teams":{"nodes":[{"id":"team_uuid_1","key":"ABC",
		  "states":{"nodes":[]}
		}]}}}`,
		`{"data":{"projectCreate":{"project":{"id":"proj_uuid_1","name":"P","url":"http://l/p1","state":"started"}}}}`,
	})
}

// callInput unmarshals the GraphQL call at index i and returns its input map.
func callInput(t *testing.T, calls *[]json.RawMessage, i int) map[string]any {
	t.Helper()
	var req struct {
		Variables struct {
			Input map[string]any `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[i], &req); err != nil {
		t.Fatalf("unmarshal call[%d]: %v", i, err)
	}
	return req.Variables.Input
}

func TestCreateProject_GroupNotFound(t *testing.T) {
	srv, _ := scriptedServer(t, []string{
		// teams(filter:{key:{eq:"NOPE"}}) returns no matches.
		`{"data":{"teams":{"nodes":[]}}}`,
	})
	b := backendForServer(srv)
	_, err := b.CreateProject(context.Background(), backends.ProjectCreateArgs{
		GroupKey: "NOPE", Name: "P",
	})
	var gnf *ErrGroupNotFound
	if !errors.As(err, &gnf) {
		t.Fatalf("err = %v, want *ErrGroupNotFound", err)
	}
	if gnf.GroupKey != "NOPE" {
		t.Errorf("GroupKey = %q, want NOPE", gnf.GroupKey)
	}
}

// ---------------- UpdateProject ----------------

func TestUpdateProject_StatusOnly_NoTeamLookup(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		`{"data":{"projectUpdate":{"project":{"id":"proj_uuid_1","state":"completed"}}}}`,
	})
	b := backendForServer(srv)
	err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Status: new("completed")})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	// Project status is workspace-level — NO team lookup needed.
	if len(*calls) != 1 {
		t.Fatalf("server saw %d calls, want 1 (no team lookup for workspace-level status)", len(*calls))
	}
	var req struct {
		Variables struct {
			ID    string         `json:"id"`
			Input map[string]any `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal((*calls)[0], &req); err != nil {
		t.Fatalf("unmarshal call[0]: %v", err)
	}
	if req.Variables.ID != "proj_uuid_1" {
		t.Errorf("variables.id = %q, want proj_uuid_1", req.Variables.ID)
	}
	if got := req.Variables.Input["state"]; got != "completed" {
		t.Errorf("input.state = %v, want completed", got)
	}
}

func TestUpdateProject_UnknownStatus(t *testing.T) {
	// projectUpdate fixture returns Linear's invalid-enum GraphQL error.
	srv, _ := scriptedServer(t, []string{
		`{"data":null,"errors":[{"message":"Variable '$input' got invalid value \"not-a-real-enum-value\"; Expected type 'ProjectStatusType'"}]}`,
	})
	b := backendForServer(srv)
	err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Status: new("not-a-real-enum-value")})
	var u *ErrUnknownState
	if !errors.As(err, &u) {
		t.Fatalf("err = %v, want *ErrUnknownState", err)
	}
	if u.GroupKey != "" {
		t.Errorf("GroupKey = %q, want empty (project state has no team scope)", u.GroupKey)
	}
	if u.State != "not-a-real-enum-value" {
		t.Errorf("State = %q, want not-a-real-enum-value", u.State)
	}
	// Defensive: never confused with auth.
	if errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v wrongly classifies as ErrUnauthorized", err)
	}
	// T4 Phase 0 retrofit: typed wrap surfaces ReasonUnknownState terminal.
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error: %v", err)
	}
	if be.Reason != backends.ReasonUnknownState || be.Transient {
		t.Errorf("typed wrap misclassified: Reason=%q Transient=%v, want %q/false",
			be.Reason, be.Transient, backends.ReasonUnknownState)
	}
}

// TestUpdateProject_Status_PreservesUnauthorized — the audit-blocking
// regression guard. 401 must propagate as ErrUnauthorized; must NOT be
// rewrapped as *ErrUnknownState even when diff.Status is non-nil.
// Sibling assertion: the same error also surfaces as a typed
// *backends.Error{Reason:auth, Transient:false}.
func TestUpdateProject_Status_PreservesUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Unauthorized"}]}`))
	}))
	t.Cleanup(srv.Close)
	b := &Backend{Client: &Client{APIKey: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}}
	err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Status: new("completed")})
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("errors.Is(err, ErrUnauthorized) = false, want true (auth must propagate). err = %v", err)
	}
	if _, ok := errors.AsType[*ErrUnknownState](err); ok {
		t.Errorf("err = %v misclassified as *ErrUnknownState — audit-blocking regression!", err)
	}
	// T4 Phase 0 retrofit: same error surfaces as typed *backends.Error.
	var be *backends.Error
	if !errors.As(err, &be) {
		t.Fatalf("errors.As did not find *backends.Error in: %v", err)
	}
	if be.Reason != backends.ReasonAuth || be.Transient {
		t.Errorf("typed wrap misclassified: Reason=%q Transient=%v, want %q/false",
			be.Reason, be.Transient, backends.ReasonAuth)
	}
}

// TestUpdateProject_Status_5xxNotMisclassified — 5xx body must not be
// rewrapped as *ErrUnknownState. The wrapped error retains "503".
func TestUpdateProject_Status_5xxNotMisclassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("service temporarily unavailable"))
	}))
	t.Cleanup(srv.Close)
	b := &Backend{Client: &Client{APIKey: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}}
	err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Status: new("completed")})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want wrapped error containing 503", err)
	}
	if _, ok := errors.AsType[*ErrUnknownState](err); ok {
		t.Errorf("err = %v misclassified as *ErrUnknownState", err)
	}
}

// TestUpdateProject_Status_TransportErrorNotMisclassified — close the
// server before the call so the underlying http.Client returns a connect
// error. Must NOT be rewrapped as *ErrUnknownState.
func TestUpdateProject_Status_TransportErrorNotMisclassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	b := &Backend{Client: &Client{APIKey: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}}
	srv.Close() // force connect failure
	err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Status: new("completed")})
	if err == nil {
		t.Fatalf("expected transport error, got nil")
	}
	if _, ok := errors.AsType[*ErrUnknownState](err); ok {
		t.Errorf("err = %v misclassified as *ErrUnknownState", err)
	}
}

// TestUpdateProject_Labels_HardErrorOnLabelCreate — UpdateProject with
// new label whose issueLabelCreate fails returns wrapped error (not nil,
// not partial). Hits the projectByID → teamByID → ensureLabels →
// issueLabelCreate path.
func TestUpdateProject_Labels_HardErrorOnLabelCreate(t *testing.T) {
	srv, reqs := opServer(t, map[string]func(int, map[string]any) string{
		"ProjectByID": func(int, map[string]any) string {
			return `{"data":{"project":{"id":"proj_uuid_1","teams":{"nodes":[{"id":"team_uuid_1","key":"ABC"}]}}}}`
		},
		"TeamByID": func(int, map[string]any) string { return teamByIDBody },
		// The lookup finds nothing, so the label is genuinely absent.
		"TeamLabelByName": func(int, map[string]any) string {
			return labelLookupBody(false)
		},
		// issueLabelCreate fails with a GraphQL error
		"IssueLabelCreate": func(int, map[string]any) string {
			return `{"data":null,"errors":[{"message":"label create rejected"}]}`
		},
		"ProjectUpdate": func(int, map[string]any) string {
			return `{"data":{"projectUpdate":{"project":{"id":"proj_uuid_1","state":"started"}}}}`
		},
	})
	b := backendForServer(srv)
	err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Labels: new("brand-new-label")})
	if err == nil {
		t.Fatalf("expected error from label-create rejection, got nil (ops: %v)", opsOf(*reqs))
	}
	if !strings.Contains(err.Error(), "brand-new-label") {
		t.Errorf("err = %v, want wrapped error mentioning the label name", err)
	}
	// Pin the arm. Error-text-only assertions would ALSO be satisfied by a
	// lookup failure, so without these this test could pass while exercising
	// a different failure entirely.
	if got := countOp(*reqs, "TeamLabelByName"); got != 1 {
		t.Errorf("filtered label lookups = %d, want 1 — this is the CREATE-failure arm (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "IssueLabelCreate"); got != 1 {
		t.Errorf("issueLabelCreate sent %d time(s), want 1 (ops: %v)", got, opsOf(*reqs))
	}
	if got := countOp(*reqs, "ProjectUpdate"); got != 0 {
		t.Errorf("projectUpdate sent %d time(s), want 0 after a label-create failure (ops: %v)", got, opsOf(*reqs))
	}
}

// ---------------- ArchiveProject ----------------

func TestArchiveProject(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		`{"data":{"projectArchive":{"success":true}}}`,
	})
	b := backendForServer(srv)
	if err := b.ArchiveProject(context.Background(), backends.RemoteRef{ID: "proj_uuid_1"}); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
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
	if req.Variables.ID != "proj_uuid_1" {
		t.Errorf("variables.id = %q, want proj_uuid_1", req.Variables.ID)
	}
}

// Compile-time guard: keep intPtr referenced (used by ticket file too,
// but also handy here for symmetry; lint won't flag because both files
// share package-scope).
var _ = intPtr
