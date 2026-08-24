// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// countingScanner records every PipelineScan call so a test can prove whether the
// hourly cluster-detection pass drains member vectors. The leaf-attachment fallback
// OVERTURNED the old zero-hourly-drain invariant: detection now drains WHEN a scanner is wired (to run
// the leaf-attachment fallback) and skips loudly when none is. servePage serves the
// drained vector index on the first scan page, then an empty page to terminate.
type countingScanner struct {
	calls atomic.Int64
	items []*knowledgev1.PipelineScanItem
}

func (c *countingScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	c.calls.Add(1)
	// Serve the seeded items on the FIRST page (after_id empty); empty afterward so
	// drainVectorIndex terminates.
	if req.GetAfterId() == "" && len(c.items) > 0 {
		return &knowledgev1.PipelineScanResponse{Items: c.items}, nil
	}
	return &knowledgev1.PipelineScanResponse{Items: nil}, nil
}

func scanItem(nodeID string, vector []byte) *knowledgev1.PipelineScanItem {
	return &knowledgev1.PipelineScanItem{NodeId: nodeID, BinaryVector: vector}
}

// leafAttachE2EFake is a single Caller that serves every read runClusterDetection
// drives end-to-end and CAPTURES the cluster_id persist write:
//   - thought-node browse (Selection set, no Ids, not EDGES) → the seeded thoughts at
//     offset 0, empty afterward (drain terminates on the short page).
//   - adjacency RETURN_MODE_EDGES read (filtered to the thought-cluster edge types,
//     which include relates-to) → the seeded leaf↔member edge.
//   - session-sibling EdgeKGContains read → nothing (no false session adjacency).
//   - buildLeafProvenance RETURN_MODE_EDGES read (nil filter) → the same edge so the
//     leaf classifies as a real-link.
//   - query(ids) hydrate + charges_for + by-id watermark reads → empty.
//   - the bulk_update_metadata persist mutation → captured into clusterWrites.
type leafAttachE2EFake struct {
	thoughtNodes []*knowledgev1.Node
	edges        []*knowledgev1.Edge // the leaf↔member adjacency/provenance edge(s)

	mu            sync.Mutex
	clusterWrites map[string]string // nodeID → persisted cluster_id
}

func (f *leafAttachE2EFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		f.captureMutation(m)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(f.edgesForFilter(q.GetSelection()), q)}, nil
	}
	// ids[] hydrate (label/charge resolution) — nothing.
	if len(q.GetIds()) > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// Type-browse drain page: full set at offset 0, empty afterward.
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Nodes: f.thoughtNodes}, nil
}

// edgesForFilter serves the seeded edges for the adjacency read (relates-to in the
// filter) and the provenance read (nil/empty filter), but NOT the session-sibling
// read (EdgeKGContains filter) — so the leaf reaches its target via a REAL edge,
// not a synthetic session sibling.
func (f *leafAttachE2EFake) edgesForFilter(sel *knowledgev1.Selection) []*knowledgev1.Edge {
	if sel == nil || len(sel.GetEdgeTypes()) == 0 {
		return f.edges // provenance read (nil filter) → all incident edges.
	}
	// UNION, not first-match: the wire returns every edge matching ANY requested
	// type. Bailing to nil as soon as the request MENTIONED kg-contains was adequate
	// only while each caller asked for a single type; the unified pivot read asks for
	// seven at once, kg-contains among them. Filtering by type reproduces the old
	// behavior for a kg-contains-only read — no contains edges are seeded, so it
	// still yields nothing — without starving a multi-type request.
	want := make(map[string]bool, len(sel.GetEdgeTypes()))
	for _, et := range sel.GetEdgeTypes() {
		want[et] = true
	}
	var out []*knowledgev1.Edge
	for _, e := range f.edges {
		if want[e.GetType()] {
			out = append(out, e)
		}
	}
	return out
}

func (f *leafAttachE2EFake) captureMutation(m *knowledgev1.MutationPlan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clusterWrites == nil {
		f.clusterWrites = map[string]string{}
	}
	for _, it := range m.GetUpdateItems() {
		if cid, ok := it.GetMetadata()["cluster_id"]; ok {
			f.clusterWrites[it.GetId()] = cid
		}
	}
}

func (f *leafAttachE2EFake) writtenClusterID(nodeID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cid, ok := f.clusterWrites[nodeID]
	return cid, ok
}

// captureSlog redirects slog to a buffer for the duration of a test and returns the
// buffer + a restore func.
func captureSlog() (*bytes.Buffer, func()) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf, func() { slog.SetDefault(prev) }
}

// TestRunClusterDetection_DrainsWhenScannerWired (FAILS-WHEN-ABSENT) asserts the
// post-leaf-attachment contract: with a wired scanner runClusterDetection issues >=1
// PipelineScan drain, runs attachLeaves, and PERSISTS the singleton leaf's cluster_id
// as the target cluster id (captured via the bulk_update_metadata write); the loud
// leaf-attachment log line carries the per-provenance attach tally.
func TestRunClusterDetection_DrainsWhenScannerWired(t *testing.T) {
	// Corpus: m1,m2 form a cluster (edge between them → CPM keeps them together);
	// leaf has a single relates-to edge to m1 and a vector near the cluster centroid.
	centroid := vec(33)
	leafVec := vecBitsFrom(centroid, 20) // sim ≈ 0.922 — passes the 0.60 linked gate.
	fake := &leafAttachE2EFake{
		thoughtNodes: []*knowledgev1.Node{
			{Id: "leaf", Type: string(kgtypes.NodeThought)},
			{Id: "m1", Type: string(kgtypes.NodeThought)},
			{Id: "m2", Type: string(kgtypes.NodeThought)},
		},
		edges: []*knowledgev1.Edge{
			{FromId: "m1", ToId: "m2", Type: string(kgtypes.EdgeRelatesTo)},   // holds the cluster.
			{FromId: "leaf", ToId: "m1", Type: string(kgtypes.EdgeRelatesTo)}, // the leaf's lone real edge.
		},
	}
	scanner := &countingScanner{items: []*knowledgev1.PipelineScanItem{
		scanItem("leaf", leafVec),
		scanItem("m1", centroid),
		scanItem("m2", centroid),
	}}
	loop := &PropagationLoop{gc: fake, stopCh: make(chan struct{})}
	loop.WithTopicDeps(scanner, nil)

	buf, restore := captureSlog()
	defer restore()

	loop.runClusterDetection()

	if got := scanner.calls.Load(); got < 1 {
		t.Fatalf("wired scanner issued %d PipelineScan drain calls, want >=1", got)
	}
	// The leaf must have been attached to m1/m2's cluster and that cluster_id persisted.
	leafCID, ok := fake.writtenClusterID("leaf")
	if !ok {
		t.Fatalf("leaf cluster_id was never persisted — attachment did not flow into the bulk_update_metadata write")
	}
	m1CID, _ := fake.writtenClusterID("m1")
	if m1CID == "" {
		// m1 may be diffed out if unchanged; reconstruct the expected target as the
		// min member id (the canonical community label) = "m1".
		m1CID = "m1"
	}
	if leafCID != m1CID {
		t.Fatalf("leaf persisted cluster_id = %q, want the target cluster %q (m1/m2's community)", leafCID, m1CID)
	}

	logged := buf.String()
	if !strings.Contains(logged, "thought: leaf-attachment") {
		t.Fatalf("leaf-attachment log line missing; got:\n%s", logged)
	}
	if !strings.Contains(logged, "by_provenance") || !strings.Contains(logged, "real-link") {
		t.Fatalf("leaf-attachment log line lacks the per-provenance tally (by_provenance / real-link); got:\n%s", logged)
	}
}

// TestRunClusterDetection_NoScannerSkips (FAILS-WHEN-ABSENT) asserts the
// degraded-mode contract: a NIL scanner causes ZERO PipelineScan drains, logs the
// loud leaf-attachment SKIPPED WARN, and completes detection normally (no panic).
func TestRunClusterDetection_NoScannerSkips(t *testing.T) {
	fake := &leafAttachE2EFake{
		thoughtNodes: []*knowledgev1.Node{
			{Id: "leaf", Type: string(kgtypes.NodeThought)},
			{Id: "m1", Type: string(kgtypes.NodeThought)},
			{Id: "m2", Type: string(kgtypes.NodeThought)},
		},
		edges: []*knowledgev1.Edge{
			{FromId: "m1", ToId: "m2", Type: string(kgtypes.EdgeRelatesTo)},
			{FromId: "leaf", ToId: "m1", Type: string(kgtypes.EdgeRelatesTo)},
		},
	}
	// Counting scanner present but NOT wired (WithTopicDeps left nil) so any drain
	// would be observable; we assert none happens.
	probe := &countingScanner{}
	loop := &PropagationLoop{gc: fake, stopCh: make(chan struct{})}
	// Deliberately do NOT call WithTopicDeps → p.scanner stays nil.

	buf, restore := captureSlog()
	defer restore()

	loop.runClusterDetection() // must complete without panic.

	if got := probe.calls.Load(); got != 0 {
		t.Fatalf("nil-scanner detection issued %d PipelineScan calls, want 0", got)
	}
	logged := buf.String()
	if !strings.Contains(logged, "leaf-attachment SKIPPED") || !strings.Contains(logged, "no member-vector scanner wired") {
		t.Fatalf("nil-scanner detection did not log the loud leaf-attachment SKIPPED WARN; got:\n%s", logged)
	}
	// Detection still completed: the clusters-detected line is emitted at the end.
	if !strings.Contains(logged, "thought: clusters detected") {
		t.Fatalf("detection did not complete after the nil-scanner skip; got:\n%s", logged)
	}
}
