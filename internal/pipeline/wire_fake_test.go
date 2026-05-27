// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// fakeWireClient is the test seam for every pipeline test that needs to
// exercise the worker / collector RPC paths. Records every RPC invocation
// (tool name + invocation count) so per-batch RPC-count assertions can be
// made cheaply. Post-FUL-305 the worker reads the server-composed text off the
// scan item directly (no node re-fetch / traverse), so the read-side helpers
// the fake serves are: listLoadedGraphs (RETURN_MODE_GRAPH_NAMES Execute) and
// scanGaps (the typed PipelineScan RPC). The write path captures items into
// recordedWrites for later inspection.
type fakeWireClient struct {
	mu sync.Mutex

	// calls records: RPC name → invocation count. "pipeline_scan" counts the
	// typed PipelineScan RPC; "mutate" counts the update_batch Execute.
	calls map[string]int

	// Captured items from every mutate(update_batch) call, in call order.
	// Each batch is appended as one element in the outer slice.
	recordedWrites [][]updateBatchItem

	// Captured ExecuteRequests, in call order. listLoadedGraphs (read) and the
	// update_batch write both ride the engine Execute seam (T-GTB6).
	execRequests []*knowledgev1.ExecuteRequest

	// graphNamesByType seeds the listLoadedGraphs query(mode:modules) per-type
	// read: graph type → its loaded graphs (served via the graph-names carrier).
	// Holds the wire proto directly (knowledgev1.GraphInfo) so the seed feeds the
	// carrier without a store→proto conversion hop.
	graphNamesByType map[string][]*knowledgev1.GraphInfo

	// scanResp seeds the typed PipelineScan RPC. Default nil → empty items +
	// dirty_gen=0 (collector no-op tick). Tests that need non-empty scan
	// results set it via seedScan.
	scanResp *knowledgev1.PipelineScanResponse
}

func newFakeWireClient() *fakeWireClient {
	return &fakeWireClient{
		calls:            make(map[string]int),
		graphNamesByType: make(map[string][]*knowledgev1.GraphInfo),
	}
}

// PipelineScan satisfies WireClient's gap-discovery seam (T-GTB4): scanGaps
// rides the typed EngineService.PipelineScan RPC rather than the legacy
// ToolService.Call. Counts the call under "pipeline_scan" and returns the
// seeded response (default: empty items, dirty_gen=0 → no-op tick).
func (f *fakeWireClient) PipelineScan(_ context.Context, _ *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.mu.Lock()
	f.calls["pipeline_scan"]++
	resp := f.scanResp
	f.mu.Unlock()
	if resp == nil {
		return &knowledgev1.PipelineScanResponse{}, nil
	}
	return resp, nil
}

// Execute satisfies WireClient's engine seam. writeBatchUpdates compiles its
// update_batch to a MUTATION_KIND_UPDATE_ITEMS MutationPlan and runs it here.
// The fake decodes the plan's UpdateItems back into updateBatchItem rows and
// records them (so the existing recordedWrites / totalWriteItems / batch-size
// assertions keep working across the repoint), counts the call as a "mutate"
// invocation (so mutateCallCount stays meaningful), and captures the full
// ExecuteRequest so tests can assert the kind + Target routing.
func (f *fakeWireClient) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	f.execRequests = append(f.execRequests, req)
	f.mu.Unlock()

	if m := req.GetMutation(); m != nil && m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS {
		f.mu.Lock()
		f.calls["mutate"]++
		items := make([]updateBatchItem, 0, len(m.GetUpdateItems()))
		for _, ui := range m.GetUpdateItems() {
			items = append(items, updateBatchItem{
				ID:           ui.GetId(),
				Summary:      ui.Summary,
				Keywords:     ui.Keywords,
				BinaryVector: ui.GetBinaryVector(),
				Metadata:     ui.GetMetadata(),
			})
		}
		f.recordedWrites = append(f.recordedWrites, items)
		f.mu.Unlock()
		return &knowledgev1.ExecuteResponse{AffectedCount: int64(len(m.GetUpdateItems()))}, nil
	}

	// Read plans: only listLoadedGraphs (RETURN_MODE_GRAPH_NAMES) rides the
	// Execute seam in the post-FUL-305 pipeline (the worker no longer re-fetches
	// nodes or traverses children — it reads the server-composed text off the
	// scan item directly).
	if q := req.GetQuery(); q != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return f.execGraphNames(req.GetTarget().GetGraph())
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// execGraphNames serves the listLoadedGraphs query(mode:modules) per-type read
// from seeded graphNamesByType via the typed GraphNames carrier. The seed already
// holds *knowledgev1.GraphInfo, so it feeds the carrier directly.
func (f *fakeWireClient) execGraphNames(graphType string) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	infos := f.graphNamesByType[graphType]
	f.mu.Unlock()
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

// mutateCallCount returns the number of mutate(update_batch) calls the
// fake observed. Used for the load-bearing per-batch RPC-count assertions
// (the test that pins 1 mutate(update_batch) per group). The mutate tool
// is the only RPC tests assert call counts on; for any other tool, peek
// at f.calls directly under the mutex.
func (f *fakeWireClient) mutateCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls["mutate"]
}

// totalWriteItems sums the lengths of every captured mutate(update_batch)
// call. Used by tests that need to verify all items were written without
// caring how they were grouped.
func (f *fakeWireClient) totalWriteItems() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, w := range f.recordedWrites {
		n += len(w)
	}
	return n
}

// seedGraphNames installs the loaded graphs of a given type for the
// listLoadedGraphs query(mode:modules) Execute read. Builds proto pointers
// (knowledgev1.GraphInfo carries protoimpl.MessageState — must never be copied
// by value, so the slice is []*knowledgev1.GraphInfo).
func (f *fakeWireClient) seedGraphNames(gt kgtypes.GraphType, names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	infos := make([]*knowledgev1.GraphInfo, len(names))
	for i, n := range names {
		infos[i] = &knowledgev1.GraphInfo{Name: n}
	}
	f.graphNamesByType[string(gt)] = infos
}

// lastExecRequest returns the last ExecuteRequest the fake observed, or nil if
// Execute was never called. Used by writeBatchUpdates tests that inspect the
// MUTATION_KIND_UPDATE_ITEMS plan + the Target selector (graph/repo/account/
// name) now that the write rides the engine Execute seam rather than gc.Call.
func (f *fakeWireClient) lastExecRequest() *knowledgev1.ExecuteRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execRequests) == 0 {
		return nil
	}
	return f.execRequests[len(f.execRequests)-1]
}

// recordedBatchSizes returns the size of every captured batch in call
// order. Used for per-batch-shape assertions.
func (f *fakeWireClient) recordedBatchSizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.recordedWrites))
	for i, w := range f.recordedWrites {
		out[i] = len(w)
	}
	return out
}

// asyncCalls is a typed-atomic call counter shared by fake summarizer /
// embedder implementations.
type asyncCalls struct{ n atomic.Int64 }

func (a *asyncCalls) inc()      { a.n.Add(1) }
func (a *asyncCalls) load() int { return int(a.n.Load()) }

// errTransient is a sentinel transient *llm.LLMError used by tests that
// exercise the transient-error retry path.
var errTransient = &llm.LLMError{Transient: true, Reason: "http_429"}

// errTerminal is a sentinel terminal *llm.LLMError used by tests that
// exercise the terminal-error marker-write path.
var errTerminal = &llm.LLMError{Transient: false, Reason: "http_400"}
