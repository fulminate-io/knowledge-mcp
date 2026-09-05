// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// The opServer harness's OWN contract, under test.
//
// WHY THIS FILE EXISTS. The harness promises that an operation no test
// scripted is a loud failure and never a canned body. That promise was once
// only a comment: the miss wrote a GraphQL errors[] response and called
// nothing on the test, the adapter turned it into an ordinary error naming the
// label, and any error-only assertion was satisfied by it. Two tests in this
// package passed that way while exercising the harness instead of the arm they
// were named for. The promise is now kept in code, so it gets a test that
// fails if it stops being kept.
//
// A test cannot observe this by handing the harness its own *testing.T: the
// t.Errorf under test would fail the very test doing the observing. So the
// harness reports through harnessReporter, and this file hands it one that
// records.

// recordingReporter captures harness failures instead of failing a test.
// Guarded by a mutex because the harness reports from the server's request
// goroutine while the test reads from its own.
type recordingReporter struct {
	mu       sync.Mutex
	failures []string
}

func (r *recordingReporter) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recordingReporter) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.failures))
	copy(out, r.failures)
	return out
}

// TestOpServer_UnscriptedOperationIsALoudFailureNamingIt drives both arms
// against ONE harness in ONE run, so the zero in the control and the one in
// the subject are produced by the same instrument.
//
// CONTROL: a SCRIPTED operation records no failure. Without it, a reporter
// that recorded on every request — or a harness that failed unconditionally —
// would satisfy the subject assertion and prove nothing.
//
// SUBJECT: an UNSCRIPTED operation records exactly one failure, and it names
// the operation, because a loud failure that does not say WHICH request was
// unanswered sends the reader looking through every handler map in the test.
func TestOpServer_UnscriptedOperationIsALoudFailureNamingIt(t *testing.T) {
	rep := &recordingReporter{}
	srv, reqs := opServerWithReporter(rep, map[string]func(int, map[string]any) string{
		// TeamByKey is scripted; TeamLabelByName deliberately is not.
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
	})
	defer srv.Close()
	b := backendForServer(srv)

	// --- control: the scripted operation ---
	team, err := b.resolveTeamByKey(context.Background(), "ABC")
	if err != nil {
		t.Fatalf("control failed: the scripted TeamByKey call errored: %v", err)
	}
	if got := rep.snapshot(); len(got) != 0 {
		t.Fatalf("control failed: a SCRIPTED operation recorded %d harness failure(s), want 0: %v", len(got), got)
	}

	// --- subject: the unscripted operation ---
	// lookupLabel issues TeamLabelByName, which this harness has no handler
	// for. It returns an error either way; the error is not what is under
	// test here, the harness's report is.
	_, _, _ = b.lookupLabel(context.Background(), team, "tools")

	failures := rep.snapshot()
	if len(failures) != 1 {
		t.Fatalf("an UNSCRIPTED operation recorded %d harness failure(s), want exactly 1 — "+
			"a silent miss is what let two tests in this package pass while exercising the harness (ops: %v)",
			len(failures), opsOf(*reqs))
	}
	if !strings.Contains(failures[0], "TeamLabelByName") {
		t.Errorf("the harness failure must NAME the unscripted operation, or the reader cannot tell which request went unanswered: %q", failures[0])
	}
}

// TestOpServer_ScriptedOperationsRecordNoFailure is the control standing on
// its own, so a regression that made the harness report on EVERY request is
// caught by a test whose name says what broke. The run above would catch it
// too, but its failure would read as a control failure rather than as what it
// is.
func TestOpServer_ScriptedOperationsRecordNoFailure(t *testing.T) {
	rep := &recordingReporter{}
	srv, _ := opServerWithReporter(rep, map[string]func(int, map[string]any) string{
		"TeamByKey": func(int, map[string]any) string { return teamByKeyBody },
		"TeamLabelByName": func(_ int, _ map[string]any) string {
			return labelLookupBody(false,
				labelMatch{ID: "label_uuid_tools", Name: "tools", TeamID: fixtureTeamID, TeamKey: fixtureTeamKey})
		},
		"IssueCreate": func(int, map[string]any) string { return issueCreateBody() },
	})
	defer srv.Close()
	b := backendForServer(srv)

	if _, err := b.CreateTicket(context.Background(), backends.TicketCreateArgs{
		GroupKey: fixtureTeamKey, Name: "T", Labels: "tools",
	}); err != nil {
		t.Fatalf("fully scripted CreateTicket errored: %v", err)
	}
	if got := rep.snapshot(); len(got) != 0 {
		t.Errorf("a fully scripted run recorded %d harness failure(s), want 0: %v", len(got), got)
	}
}
