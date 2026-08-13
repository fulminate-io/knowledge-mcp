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
// made cheaply. The worker reads the server-composed text off the
// scan item directly (no node re-fetch / traverse), so the read side the fake
// serves is scanGaps (the typed PipelineScan RPC), plus a generic graph-names
// Execute the pipeline itself no longer issues. The write path captures items
// into recordedWrites for later inspection.
type fakeWireClient struct {
	mu sync.Mutex

	// calls records: RPC name → invocation count. "pipeline_scan" counts the
	// typed PipelineScan RPC; "mutate" counts the update_batch Execute.
	calls map[string]int

	// Captured items from every mutate(update_batch) call, in call order.
	// Each batch is appended as one element in the outer slice.
	recordedWrites [][]updateBatchItem

	// Captured ExecuteRequests, in call order. The update_batch write rides the
	// engine Execute seam; a graph-names read would too, which is why counting
	// these is how a test proves the catalog is never read.
	execRequests []*knowledgev1.ExecuteRequest

	// graphNamesByType seeds the generic graph-names read: graph type → its loaded
	// graphs (served via the graph-names carrier). Holds the wire proto directly
	// (knowledgev1.GraphInfo) so the seed feeds the carrier without a store→proto
	// conversion hop.
	//
	// NOTHING IN THE PIPELINE READS THIS ANY MORE. The catalog pass takes its
	// wanted set from the working set, so a seeded catalog exists to be the
	// non-empty backend a test proves is NEVER consulted — the known-positive
	// control that keeps "zero catalog reads" from being a vacuous zero.
	graphNamesByType map[string][]*knowledgev1.GraphInfo

	// scanResp seeds the typed PipelineScan RPC. Default nil → empty items +
	// dirty_gen=0 (collector no-op tick). Tests that need non-empty scan
	// results set it via seedScan.
	scanResp *knowledgev1.PipelineScanResponse

	// genPollResp seeds the bulk PipelineGenPoll RPC. Default nil → an empty
	// PipelineGenPollResponse (no entries → no (graph,axis) advances → 0 Phase-2
	// detail fetches). Tests that need the gen-poll to report advanced gens set it
	// via seedGenPoll.
	genPollResp *knowledgev1.PipelineGenPollResponse

	// genPollCatalogGen is the account CATALOG watermark every PipelineGenPoll
	// response carries. Default 0 — the value a server serves before its first
	// bump, which the client's zero-skip ignores — so tests that seed only entries
	// (seedGenPoll) never trip the catalog compare. Set via seedGenPollCatalog.
	genPollCatalogGen uint64

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

	// scansByGraph records pipeline_scan invocation counts keyed by the request's
	// GRAPH NAME, so a test can assert that a never-interacted graph was never
	// scanned while an admitted one was — the same measurement on both, which is
	// what stops the zero being indistinguishable from an undriven fake.
	scansByGraph map[string]int

	// genPollRequests captures every PipelineGenPollRequest in call order. The
	// gen-poll's scoping lives in the REQUEST the client builds from its collector
	// registry, so asserting the request is what proves a foreign graph is absent;
	// asserting the response would only describe what the fake chose to say.
	genPollRequests []*knowledgev1.PipelineGenPollRequest
}

func newFakeWireClient() *fakeWireClient {
	return &fakeWireClient{
		calls:            make(map[string]int),
		graphNamesByType: make(map[string][]*knowledgev1.GraphInfo),
		scansByAxis:      make(map[string]int),
		scansByGraph:     make(map[string]int),
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
	f.scansByGraph[req.GetGraphName()]++
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

// PipelineGenPoll satisfies WireClient's bulk Phase-1 gen-poll seam: the central
// gen-poll loop issues ONE of these per tick. Counts the call under
// "pipeline_gen_poll" and returns the seeded response (default: an empty response
// → no entries → no advances → 0 Phase-2 detail fetches).
func (f *fakeWireClient) PipelineGenPoll(_ context.Context, req *knowledgev1.PipelineGenPollRequest) (*knowledgev1.PipelineGenPollResponse, error) {
	f.mu.Lock()
	f.calls["pipeline_gen_poll"]++
	f.genPollRequests = append(f.genPollRequests, req)
	resp := f.genPollResp
	catalogGen := f.genPollCatalogGen
	f.mu.Unlock()
	if resp == nil {
		return &knowledgev1.PipelineGenPollResponse{CatalogGen: catalogGen}, nil
	}
	return &knowledgev1.PipelineGenPollResponse{Entries: resp.GetEntries(), CatalogGen: catalogGen}, nil
}

// seedGenPoll installs the response returned for the bulk PipelineGenPoll RPC,
// modeling a server that reports the given per-(graph,axis) dirty-gen entries.
// The central gen-poll loop diffs each entry's gen against its watermark and
// fires a Phase-2 PipelineScan for every pair that advanced.
func (f *fakeWireClient) seedGenPoll(entries ...*knowledgev1.PipelineGenPollEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.genPollResp = &knowledgev1.PipelineGenPollResponse{Entries: entries}
}

// seedGenPollCatalog installs both the per-(graph,axis) entries AND the account
// CATALOG watermark the bulk PipelineGenPoll response carries, modeling a server
// whose graph catalog has moved. Separate from seedGenPoll so that helper's
// signature stays exactly as its callers (and the landed graph criterion pinning
// it) expect; a test that does not care about the catalog keeps getting 0.
func (f *fakeWireClient) seedGenPollCatalog(catalogGen uint64, entries ...*knowledgev1.PipelineGenPollEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.genPollResp = &knowledgev1.PipelineGenPollResponse{Entries: entries}
	f.genPollCatalogGen = catalogGen
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

	// The generic graph-names read still has a server (a seeded catalog is what a
	// test points at to prove the pipeline never asks for it), but no pipeline code
	// path issues one any more.
	if q := req.GetQuery(); q != nil {
		if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
			return f.execGraphNames(req.GetTarget().GetGraph())
		}
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// execGraphNames serves a graph-names read from seeded graphNamesByType via the
// typed GraphNames carrier. The seed already holds *knowledgev1.GraphInfo, so it
// feeds the carrier directly.
func (f *fakeWireClient) execGraphNames(graphType string) (*knowledgev1.ExecuteResponse, error) {
	f.mu.Lock()
	infos := f.graphNamesByType[graphType]
	f.mu.Unlock()
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

// genPollCallCount returns how many bulk PipelineGenPoll RPCs the fake observed,
// read under the mutex. Tests that drive the gen-poll loop goroutine must go
// through here rather than reading f.calls directly: the loop writes that map
// concurrently with the assertion.
func (f *fakeWireClient) genPollCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls["pipeline_gen_poll"]
}

// codeGraphNamesReads returns how many graph-names Executes the fake observed for
// the CODE graph type, read under the mutex (the catalog loop goroutine appends to
// execRequests concurrently). The pipeline no longer issues these at all, so this
// is now a ZERO-detector: a non-zero reading means catalog enumeration came back.
func (f *fakeWireClient) codeGraphNamesReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, req := range f.execRequests {
		q := req.GetQuery()
		if q == nil || q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
			continue
		}
		if req.GetTarget().GetGraph() == string(kgtypes.GraphCode) {
			n++
		}
	}
	return n
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

// seedGraphNames installs the loaded graphs of a given type for the graph-names
// Execute read — in practice, the non-empty backend catalog a test proves the
// pipeline never consults. Builds proto pointers
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
// exercise the terminal-error marker-write path. Reason "http_400" classifies to
// ClassInvalidRequest, which IS deterministic-terminal — so two consecutive
// errTerminal failures of one axis FAST-TRIP the breaker at
// DefaultDeterministicFastTripThreshold. Tests that drive a multi-call
// zero-success WINDOW (rather than the fast-trip) must use
// errTerminalNonDeterministic instead so the deterministic streak never fires.
var errTerminal = &llm.LLMError{Transient: false, Reason: "http_400"}

// errTerminalNonDeterministic is a sentinel terminal *llm.LLMError whose Reason
// ("http_401") classifies to ClassAuthQuota — a TERMINAL but NON-deterministic
// class (only 429/5xx are transient, so 401 takes the terminal marker-write path
// without a backoff delay). It is the fixture for per-axis zero-success WINDOW
// tests (many consecutive errored calls WITHOUT the deterministic fast-trip
// short-circuiting the run) and for the auth/quota shared-cause escalation tests.
var errTerminalNonDeterministic = &llm.LLMError{Transient: false, Reason: "http_401"}
