// SPDX-License-Identifier: Apache-2.0

package tools

// fake_graph_caller_record_test.go holds fakeGraphCaller's CALL LOG: the
// recordedCall carrier and the four guarded appenders every seam method records
// through. Split out of fake_graph_caller_test.go to keep both files inside the
// repo's file-length gate, along the seam between SERVING a scripted response and
// RECORDING that it was asked for.
//
// THE APPENDS ARE GUARDED because several composers this fake drives fan their
// reads out CONCURRENTLY — searchAllQueries runs one goroutine per query — so two
// Execute calls reach these appends at the same time. Unguarded that is a data race
// the -race build reports, and it can also silently drop a recorded call from a
// slice a test then asserts on. The seeded response maps are NOT guarded and do not
// need to be: they are written once at fixture construction and only read after.

import (
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

type recordedCall struct {
	tool string
	args json.RawMessage
}

// recordCall appends one entry to the call log under recordMu. Every site that
// records a call goes through it, so no caller can reintroduce a bare append.
func (f *fakeGraphCaller) recordCall(c recordedCall) {
	f.recordMu.Lock()
	f.calls = append(f.calls, c)
	f.recordMu.Unlock()
}

// recordExec appends one ExecuteRequest to the request log under recordMu.
func (f *fakeGraphCaller) recordExec(req *knowledgev1.ExecuteRequest) {
	f.recordMu.Lock()
	f.execRequests = append(f.execRequests, req)
	f.recordMu.Unlock()
}

// recordMutation appends one MutationPlan plus its "mutate" call-log entry and
// returns that mutation's 1-BASED ORDINAL. The ordinal is produced INSIDE the same
// critical section as the append because mutateErrOnNth selects on it: reading the
// length afterwards would let a concurrent mutation shift which call the ordinal
// knob fires for.
func (f *fakeGraphCaller) recordMutation(m *knowledgev1.MutationPlan) int {
	f.recordMu.Lock()
	defer f.recordMu.Unlock()
	f.execMutations = append(f.execMutations, m)
	f.calls = append(f.calls, recordedCall{tool: "mutate"})
	return len(f.execMutations)
}

// recordStats appends one StatsRequest plus its "stats" call-log entry.
func (f *fakeGraphCaller) recordStats(req *knowledgev1.StatsRequest) {
	f.recordMu.Lock()
	f.statsReqs = append(f.statsReqs, req)
	f.calls = append(f.calls, recordedCall{tool: "stats"})
	f.recordMu.Unlock()
}
