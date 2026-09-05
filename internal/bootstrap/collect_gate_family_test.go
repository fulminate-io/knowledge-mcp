// SPDX-License-Identifier: Apache-2.0

package bootstrap

// collect_gate_family_test.go is the regression proof for the defect this gate
// change exists to remove: a FIRST-EVER collect into a non-code graph had its own
// pipeline lane scan the graph mid-upload, read graph-not-found, classify it
// durable and evict the graph.
//
// bootstrap is the only package where the real CollectRuntime and the real
// Pipeline meet, and attachCollectGate — the PRODUCTION wiring, not a hand-built
// predicate — lives here.
//
// THE VERDICT IS ONE BLOCKING SELECT over three one-shot channels: gate-held,
// scan-issued, graph-evicted. Whichever arrives FIRST decides, so a gate that is
// inert for this family is REJECTED rather than waited out. No timer and no sleep
// participates, so the test measures ordering rather than duration.

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// gateHoldMarker is the production observable for a gate hold, written as a
// LITERAL rather than shared with the emitting code: sharing a constant would let
// a reworded marker stay green here while breaking the operator's evidence trail.
// It is emitted at Debug (pipeline.collector's noteGateTransition), which is the
// level the daemon runs at.
const gateHoldMarker = "pipeline.collector: gap scan gated by in-flight collect"

// errGraphNotFoundForTest is the inner error the fake wire client wraps in a
// CodeNotFound connect error — the server's answer while a first-ever collect is
// still uploading.
var errGraphNotFoundForTest = errors.New("graph not found")

// gateHoldHandler is a slog.Handler that closes held the FIRST time it sees the
// gate-hold record, keeping that record's graph_type attribute.
type gateHoldHandler struct {
	slog.Handler
	once sync.Once
	held chan struct{}

	mu        sync.Mutex
	graphType string
}

func (h *gateHoldHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *gateHoldHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != gateHoldMarker {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "graph_type" {
			h.mu.Lock()
			h.graphType = a.Value.String()
			h.mu.Unlock()
			return false
		}
		return true
	})
	h.once.Do(func() { close(h.held) })
	return nil
}

func (h *gateHoldHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *gateHoldHandler) WithGroup(string) slog.Handler      { return h }

func (h *gateHoldHandler) capturedGraphType() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.graphType
}

// notFoundWireClient implements pipeline.WireClient — exactly the three methods
// that interface declares. It deliberately does NOT implement CorpusDelta: that
// is the separate optional CorpusDeltaScanner seam, and implementing it would
// activate the BM25 arm against this fake.
//
// PipelineScan answers CodeNotFound, which is what the server answers while a
// first-ever collect is still uploading, and is the exact code the collector's
// durable-not-found classifier acts on.
type notFoundWireClient struct {
	once    sync.Once
	scanned chan struct{}
}

func (c *notFoundWireClient) PipelineGenPoll(context.Context, *knowledgev1.PipelineGenPollRequest) (*knowledgev1.PipelineGenPollResponse, error) {
	return &knowledgev1.PipelineGenPollResponse{}, nil
}

func (c *notFoundWireClient) PipelineScan(context.Context, *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	c.once.Do(func() { close(c.scanned) })
	return nil, connect.NewError(connect.CodeNotFound, errGraphNotFoundForTest)
}

func (c *notFoundWireClient) Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestCollectGate_HoldsAFirstEverPDFCollect observes the running system end to
// end: a real collect runtime holding a pdf collect open, the production
// attachCollectGate wiring, a real pipeline collector registered for that pdf
// graph, a wire client answering NotFound and the real durable-not-found eviction
// arm.
func TestCollectGate_HoldsAFirstEverPDFCollect(t *testing.T) {
	prior := slog.Default()
	handler := &gateHoldHandler{Handler: prior.Handler(), held: make(chan struct{})}
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prior) })

	// THE IDENTITY COMES FROM PRODUCTION. A hand-written name would make both
	// sides of the gate's equality this test's own.
	pdfPath, err := filepath.Abs("../collector/pdf/testdata/form_xobject.pdf")
	require.NoError(t, err)
	graphName, err := tools.CollectGateGraphName("pdf", pdfPath, nil)
	require.NoError(t, err, "the pdf derivation must not refuse an absolute path")
	require.NotEmpty(t, graphName, "a pdf collect must derive a gate identity")

	rt := tools.NewCollectRuntime()
	// REGISTERED BEFORE the release cleanup so LIFO ordering runs the release
	// close FIRST and the work function is unblocked before Stop waits on it.
	// CollectRuntime.Stop takes a deadline, so this is a closure and not a bare
	// method value.
	t.Cleanup(func() { rt.Stop(2 * time.Second) })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	_, started, _ := rt.Start("pdf\x00"+pdfPath, "pdf "+pdfPath, kgtypes.GraphPDFRaw, graphName,
		func() (string, string, error) {
			// Blocking, so the precondition — a collect IS in flight — is held
			// open unconditionally rather than raced against. Without it the run
			// ends and the gate lowers before the collector's first tick.
			<-release
			return "", "", nil
		})
	require.True(t, started)

	// IDENTITY CONTROLS, before the pipeline is built. The known-positive: without
	// it the select's green would prove nothing, since a gate that never fires and
	// a collector that never scanned look alike.
	require.True(t, rt.CollectInFlightForGraph(kgtypes.GraphPDFRaw, graphName),
		"a pdf collect in flight must gate its own pdf graph")
	// The CROSS-FAMILY control: this is what fails if dropping the GraphCode-only
	// guard degenerated into name-only matching.
	require.False(t, rt.CollectInFlightForGraph(kgtypes.GraphCode, graphName),
		"a pdf collect must not gate a CODE graph that shares its name")

	wc := &notFoundWireClient{scanned: make(chan struct{})}
	cfg := pipeline.Config{
		SummaryChannelSize: 4,
		SummaryBatchSize:   1,
		SummaryWorkers:     1,
		Tick:               5 * time.Millisecond,
	}
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	p := pipeline.New(cfg, wc, noopSum, nil)

	evicted := make(chan struct{})
	var evictOnce sync.Once
	p.AttachGraphEvictor(func(kgtypes.GraphType, string, string) {
		evictOnce.Do(func() { close(evicted) })
	})

	// THE PRODUCTION WIRING, not a hand-built predicate.
	attachCollectGate(p, &client{collectRuntime: rt})

	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Stop(stopCtx)
	})
	p.RegisterGraph(context.Background(), kgtypes.GraphPDFRaw, graphName)

	select {
	case <-handler.held:
		// The gate held. This is the pass.
	case <-wc.scanned:
		t.Fatal("the pipeline issued a gap scan for a graph whose FIRST collect is still uploading — " +
			"the collect gate is inert for this family")
	case <-evicted:
		t.Fatal("the graph was evicted by its own collect's lane — " +
			"the gap scan read graph-not-found mid-upload and classified it durable")
	}

	// Ticket in-scope item 5: the gate-hold marker must NAME the family. No
	// production edit is needed for it — noteGateTransition already logs
	// graph_type — so this pins it rather than adding it.
	require.Equal(t, string(kgtypes.GraphPDFRaw), handler.capturedGraphType(),
		"the gate-hold marker must name the family the gate held for")
}
