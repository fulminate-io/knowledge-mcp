// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// ============================================================
// Harness: a fake tracker that routes by GraphQL OPERATION NAME.
//
// scriptedServer (backend_read_test.go:27) is POSITIONAL — it hands back
// responses[i] for call i and reuses the last one past the end. That shape
// cannot answer "resolve the team, then answer a per-label lookup, then
// answer the write", because inserting one lookup shifts every later
// response. opServer routes on the operation name in the request's query
// text instead, so a test scripts BEHAVIOUR per operation and the call
// order is free. Every request is recorded (op, raw query text, decoded
// variables) so a test can assert on what went OUT, not only on what came
// back — the precedent for reading the outgoing body is client_test.go:80
// (query text) and :94 (encoded variables).
//
// An operation with no handler FAILS THE TEST, naming the operation, so a
// test that provoked an unscripted request says so rather than silently
// passing on a default. A GraphQL error body IS still written after that
// report, because the client is waiting on a reply and hanging it would only
// obscure the failure — but that body can no longer be mistaken for a
// scripted answer, since the test is already failed by the time the adapter
// reads it.
//
// Generic placeholders only — never real Linear team/workspace identifiers
// in fixtures (backend_read_test.go:25-26).
// ============================================================

// ============================================================
// WHAT THESE TESTS CANNOT OBSERVE, said out loud so a later reader does not
// mistake their scope.
//
// The defect this ticket fixes was a PAGE POSITION: the team's labels were
// read in one bulk page, and a label the team held past that page looked
// absent, was re-created, and Linear rejected the create as a duplicate — so
// the whole write was lost. That bulk read is GONE. With a per-name filtered
// lookup there is no page for a label to fall off, and page position is no
// longer expressible in-process at all: the fake answers a filter, not an
// offset. These tests therefore assert the SHAPE that makes page position
// irrelevant (one filtered lookup per name, the comparison performed by the
// tracker), not the old boundary.
//
// The live evidence that the shape actually resolves the labels the failures
// died on is the validator's read-only close-out sweep against the real team
// (recorded on the ticket): all seven labels from the recorded observations —
// tools, testing, collect, server, agent, helm, docs — return EXACTLY ONE
// match under both their exact and a case-differing spelling, same label id
// both ways, hasNextPage false, with bug-hunt, collectors and a nonsense name
// returning zero in the same run as controls. Confirming it end to end on the
// real tracker is the post-merge live check, named rather than substituted.
// ============================================================

var opNameRE = regexp.MustCompile(`(?s)^\s*(?:query|mutation)\s+([A-Za-z0-9_]+)`)

// dropConnection is the sentinel a handler returns to make opServer hijack
// and close the connection without a reply, which surfaces in Client.do as
// a transport failure from c.HTTP.Do (client.go:153-165). Returning a body
// cannot express that arm.
const dropConnection = "\x00DROP-CONNECTION\x00"

type recordedReq struct {
	Op    string
	Query string
	Vars  map[string]any
}

// harnessReporter is the sink opServer reports its own failures through.
// *testing.T satisfies it, which is what every real test hands over.
//
// It exists so the harness's loud-failure contract is TESTABLE. A test that
// asserted t.Errorf had fired would be failed by that very call, so the only
// way to observe the contract is to hand the harness a sink that records
// instead of failing. See label_lookup_harness_contract_test.go.
type harnessReporter interface {
	Errorf(format string, args ...any)
}

func opServer(t *testing.T, handlers map[string]func(callN int, vars map[string]any) string) (*httptest.Server, *[]recordedReq) {
	t.Helper()
	srv, reqs := opServerWithReporter(t, handlers)
	t.Cleanup(srv.Close)
	return srv, reqs
}

// opServerWithReporter is opServer with the failure sink injected and no
// cleanup registered, so a caller that is not a *testing.T can drive it. Every
// ordinary test uses opServer, which hands over its own t and closes the
// server on cleanup.
func opServerWithReporter(rep harnessReporter, handlers map[string]func(callN int, vars map[string]any) string) (*httptest.Server, *[]recordedReq) {
	reqs := make([]recordedReq, 0, 8)
	counts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var env struct {
			Query string         `json:"query"`
			Vars  map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(raw, &env)
		op := ""
		if m := opNameRE.FindStringSubmatch(env.Query); len(m) == 2 {
			op = m[1]
		}
		reqs = append(reqs, recordedReq{Op: op, Query: env.Query, Vars: env.Vars})
		h, ok := handlers[op]
		if !ok {
			// LOUD, as the contract above promises, and t.Errorf is what
			// makes it so. Writing only a GraphQL errors[] body is NOT
			// enough: the adapter turns that into an ordinary error that
			// names the label, which satisfies any error-only assertion and
			// lets a test pass while exercising the harness instead of the
			// arm it is named for. Two tests in this package did exactly
			// that before this call existed.
			//
			// t.Errorf is safe from this goroutine; t.Fatalf would not be.
			named := op
			if named == "" {
				named = "<no operation name in the request>"
			}
			rep.Errorf("TEST HARNESS: unscripted GraphQL operation %s reached the fake. "+
				"Script a handler for it, or assert it is never sent — as it stands, "+
				"nothing under test answered this request.", named)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"errors":[{"message":"TEST HARNESS: no handler for operation ` + op + `"}]}`))
			return
		}
		n := counts[op]
		counts[op] = n + 1
		body := h(n, env.Vars)
		if body == dropConnection {
			hj, canHijack := w.(http.Hijacker)
			if !canHijack {
				rep.Errorf("TEST HARNESS: ResponseWriter is not a Hijacker; cannot drop the connection for %s", op)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				rep.Errorf("TEST HARNESS: hijack for %s: %v", op, err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	return srv, &reqs
}

// opsOf renders the recorded operation sequence for failure messages.
func opsOf(reqs []recordedReq) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Op)
	}
	return out
}

// countOp counts recorded requests for one operation.
func countOp(reqs []recordedReq, op string) int {
	n := 0
	for _, r := range reqs {
		if r.Op == op {
			n++
		}
	}
	return n
}

// firstIndexOfOp / lastIndexOfOp locate an operation in the recorded SEQUENCE.
// Counting operations cannot express "every lookup happened before any create";
// ordering can, and that ordering is the whole of the resolve-all contract.
func firstIndexOfOp(reqs []recordedReq, op string) int {
	for i, r := range reqs {
		if r.Op == op {
			return i
		}
	}
	return -1
}

func lastIndexOfOp(reqs []recordedReq, op string) int {
	last := -1
	for i, r := range reqs {
		if r.Op == op {
			last = i
		}
	}
	return last
}

// reqsFor returns every recorded request for one operation.
func reqsFor(reqs []recordedReq, op string) []recordedReq {
	out := make([]recordedReq, 0, len(reqs))
	for _, r := range reqs {
		if r.Op == op {
			out = append(out, r)
		}
	}
	return out
}

// ---------------- fixture bodies ----------------

// fixtureTeamID / fixtureTeamKey are the generic placeholders every fixture
// here shares — never real Linear identifiers.
const (
	fixtureTeamID  = "team_uuid_1"
	fixtureTeamKey = "ABC"
)

// teamByKeyBody is the create-path team resolution response. It carries NO
// labels key: the team queries select id/key/states only, so a fixture
// offering labels would describe a selection the query never makes.
const teamByKeyBody = `{"data":{"teams":{"nodes":[{"id":"team_uuid_1","key":"ABC",
  "states":{"nodes":[{"id":"state_uuid_1","name":"Todo"}]}}]}}}`

// teamByIDBody is the update-path team resolution response, same selection.
const teamByIDBody = `{"data":{"team":{"id":"team_uuid_1","key":"ABC",
  "states":{"nodes":[{"id":"state_uuid_1","name":"Todo"}]}}}}`

// labelMatch is one row of the filtered label lookup. TeamKey empty means
// the label is WORKSPACE-scoped (Linear returns team: null for those).
type labelMatch struct {
	ID      string
	Name    string
	TeamID  string
	TeamKey string
}

// labelLookupBody renders the filtered-lookup response: the team envelope,
// pageInfo.hasNextPage, and the matched label nodes with their scope.
func labelLookupBody(hasNextPage bool, matches ...labelMatch) string {
	nodes := make([]string, 0, len(matches))
	for _, m := range matches {
		team := "null"
		if m.TeamKey != "" {
			team = `{"id":"` + m.TeamID + `","key":"` + m.TeamKey + `"}`
		}
		nodes = append(nodes, `{"id":"`+m.ID+`","name":"`+m.Name+`","team":`+team+`}`)
	}
	return fmt.Sprintf(`{"data":{"team":{"id":%q,"key":%q,"labels":{"pageInfo":{"hasNextPage":%t},"nodes":[%s]}}}}`,
		fixtureTeamID, fixtureTeamKey, hasNextPage, strings.Join(nodes, ","))
}

func issueCreateBody() string {
	return `{"data":{"issueCreate":{"issue":{"id":"issue_uuid_1","identifier":"ABC-1","title":"T","url":"http://l/i1","state":{"name":"Todo"}}}}}`
}

func issueLabelCreateBody(name string) string {
	return `{"data":{"issueLabelCreate":{"issueLabel":{"id":"label_uuid_created","name":"` + name + `"}}}}`
}

// assertNoBulkLabelRead fails when the recorded team-resolution request named
// by teamOp still carries a labels( selection. R1 removes the bulk label read
// from BOTH team queries, and nothing about removing the Labels struct field
// forces the query text to change with it: encoding/json drops response keys
// with no matching field, so a query still asking for 250 labels decodes and
// behaves identically. Only the request AS SENT shows it.
//
// The recorded lookup request is the same-run KNOWN-POSITIVE control: it does
// carry a labels( selection, so an absence reported here is a real absence and
// not a blind instrument.
//
// Call this for EVERY team query a path issues. An assertion written against
// one constant says nothing about the other — teamByKeyQuery and teamByIDQuery
// are separate strings, and the create paths reach the first while the update
// paths reach the second.
func assertNoBulkLabelRead(t *testing.T, reqs []recordedReq, teamOp string) {
	t.Helper()
	lookups := reqsFor(reqs, "TeamLabelByName")
	if len(lookups) == 0 {
		t.Fatalf("no filtered lookup was recorded, so the absence check has no known-positive control (ops: %v)", opsOf(reqs))
	}
	if !strings.Contains(lookups[0].Query, "labels(") {
		t.Fatalf("control failed: the lookup query carries no labels( selection, so the absence assertion below proves nothing:\n%s", lookups[0].Query)
	}
	teamReqs := reqsFor(reqs, teamOp)
	if len(teamReqs) != 1 {
		t.Fatalf("%s requests = %d, want 1 (ops: %v)", teamOp, len(teamReqs), opsOf(reqs))
	}
	if strings.Contains(teamReqs[0].Query, "labels(") {
		t.Errorf("%s still carries a bulk labels selection; the filtered lookup replaces it:\n%s", teamOp, teamReqs[0].Query)
	}
}

// labelIDsOf decodes input.labelIds off a recorded mutation request.
func labelIDsOf(t *testing.T, req recordedReq) []string {
	t.Helper()
	input, ok := req.Vars["input"].(map[string]any)
	if !ok {
		t.Fatalf("%s variables carry no input map: %v", req.Op, req.Vars)
	}
	raw, ok := input["labelIds"].([]any)
	if !ok {
		t.Fatalf("%s input.labelIds missing or not a list: %v", req.Op, input["labelIds"])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s input.labelIds carries a non-string: %v", req.Op, v)
		}
		out = append(out, s)
	}
	return out
}

// asciiFoldEqual reports whether a and b are equal under an ASCII-ONLY case
// fold — the narrower comparator indexOfSameLabel could be reduced to. It
// exists purely as a fixture control, so a fold test can show it discriminates
// between the two candidate implementations rather than passing under both.
// Production code never calls it.
func asciiFoldEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
