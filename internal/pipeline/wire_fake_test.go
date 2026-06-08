// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// fakeWireClient is the test seam for every pipeline test that needs to
// exercise the worker / collector RPC paths. Records every RPC invocation
// (tool name + invocation count) so per-batch RPC-count assertions can be
// made cheaply. The worker reads the server-composed text off the
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
	// update_batch write both ride the engine Execute seam.
	execRequests []*knowledgev1.ExecuteRequest

	// graphNamesByType seeds the listLoadedGraphs query(mode:modules) per-type
	// read: graph type → its loaded graphs (served via the graph-names carrier).
	// Holds the wire proto directly (knowledgev1.GraphInfo) so the seed feeds the
	// carrier without a store→proto conversion hop.
	graphNamesByType map[string][]*knowledgev1.GraphInfo

	// failGraphTypes marks graph types whose listLoadedGraphs Execute read should
	// return an error (a backend rollout 502, a permission_denied). Used to prove
	// per-type failures are non-fatal: enumeration skips the type and continues.
	failGraphTypes map[string]bool

	// rateLimitGraphTypes marks graph types whose listLoadedGraphs Execute read
	// returns a connect.CodeUnavailable ("Too many requests") error carrying a
	// Retry-After of the mapped seconds (0 = no hint). Used to prove a whole-tick
	// remote 429 is classified as a throttle so the discovery loop backs off.
	rateLimitGraphTypes map[string]int

	// scanResp seeds the typed PipelineScan RPC. Default nil → empty items +
	// dirty_gen=0 (collector no-op tick). Tests that need non-empty scan
	// results set it via seedScan.
	scanResp *knowledgev1.PipelineScanResponse

	// embedScanResp / summaryScanResp, when non-nil, are returned for the
	// matching axis's scans (req.axis == "embed" / "summary") INSTEAD of scanResp.
	// Lets a test seed eligible items on ONE axis only while the other returns
	// empty — used to prove a given axis is (or is not) being scanned.
	embedScanResp   *knowledgev1.PipelineScanResponse
	summaryScanResp *knowledgev1.PipelineScanResponse

	// scansByAxis records pipeline_scan invocation counts keyed by the request
	// axis ("summary" | "embed"). The flat calls["pipeline_scan"] counts both
	// axes together; this split lets a test assert that the embed axis was never
	// scanned (the embed-gate proof) while the summary axis flowed.
	scansByAxis map[string]int
}

func newFakeWireClient() *fakeWireClient {
	return &fakeWireClient{
		calls:               make(map[string]int),
		graphNamesByType:    make(map[string][]*knowledgev1.GraphInfo),
		failGraphTypes:      make(map[string]bool),
		rateLimitGraphTypes: make(map[string]int),
		scansByAxis:         make(map[string]int),
	}
}

// PipelineScan satisfies WireClient's gap-discovery seam: scanGaps
// rides the typed EngineService.PipelineScan RPC rather than the legacy
// ToolService.Call. Counts the call under "pipeline_scan" and returns the
// seeded response (default: empty items, dirty_gen=0 → no-op tick).
func (f *fakeWireClient) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	axis := req.GetAxis()
	f.mu.Lock()
	f.calls["pipeline_scan"]++
	f.scansByAxis[axis]++
	resp := f.scanResp
	switch {
	case axis == "embed" && f.embedScanResp != nil:
		resp = f.embedScanResp
	case axis == "summary" && f.summaryScanResp != nil:
		resp = f.summaryScanResp
	}
	f.mu.Unlock()
	if resp == nil {
		return &knowledgev1.PipelineScanResponse{}, nil
	}
	return resp, nil
}

// scanCountForAxis returns how many pipeline_scan RPCs the fake observed for the
// given axis ("summary" | "embed"). Used by the embed-gate test to assert the
// embed axis was never scanned when no embedder is configured.
func (f *fakeWireClient) scanCountForAxis(axis string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scansByAxis[axis]
}

// seedEmbedScan installs the response returned for embed-axis scans only,
// modeling a server that has embed-eligible gaps. Each item carries a NodeID +
// server-composed EmbedText so a running embed loop would push real EmbedWork.
func (f *fakeWireClient) seedEmbedScan(items ...*knowledgev1.PipelineScanItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embedScanResp = &knowledgev1.PipelineScanResponse{Items: items, DirtyGen: 1}
}

// seedSummaryScan installs the response returned for summary-axis scans only,
// modeling a server that has summary-eligible gaps. Each item carries a NodeID +
// server-composed SummarizeText so a running summary loop would push real
// SummaryWork.
func (f *fakeWireClient) seedSummaryScan(items ...*knowledgev1.PipelineScanItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaryScanResp = &knowledgev1.PipelineScanResponse{Items: items, DirtyGen: 1}
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
	// Execute seam in the pipeline (the worker no longer re-fetches
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
	failed := f.failGraphTypes[graphType]
	retryAfter, rateLimited := f.rateLimitGraphTypes[graphType]
	infos := f.graphNamesByType[graphType]
	f.mu.Unlock()
	if rateLimited {
		ce := connect.NewError(connect.CodeUnavailable, fmt.Errorf("Too many requests. Please slow down."))
		if retryAfter > 0 {
			ce.Meta().Set("Retry-After", strconv.Itoa(retryAfter))
		}
		return nil, ce
	}
	if failed {
		return nil, fmt.Errorf("fake: list-graphs unavailable for %s (simulated rollout)", graphType)
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

// failGraphNames marks a graph type's listLoadedGraphs Execute read to error,
// simulating a per-type backend failure (rollout 502, permission_denied).
func (f *fakeWireClient) failGraphNames(gt kgtypes.GraphType) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failGraphTypes[string(gt)] = true
}

// rateLimitGraphNames marks a graph type's listLoadedGraphs Execute read to
// return a connect.CodeUnavailable 429 carrying retryAfterSecs as Retry-After
// (0 = no hint). Simulates the backend per-IP throttle the discovery loop must
// classify as a whole-tick throttle and back off from.
func (f *fakeWireClient) rateLimitGraphNames(gt kgtypes.GraphType, retryAfterSecs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rateLimitGraphTypes[string(gt)] = retryAfterSecs
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
