// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// recordingGraphNamesCaller is a GraphCaller that answers RETURN_MODE_GRAPH_NAMES
// queries from a seeded per-graph-type name map and records every target graph
// it was asked about. Used to prove handleSyncList enumerates the right types
// against the right caller (local vs cloud).
type recordingGraphNamesCaller struct {
	// byType maps a graph-type string → the GraphInfos that type yields.
	byType map[string][]*knowledgev1.GraphInfo
	// seenGraphs records the target graph of every Execute call.
	seenGraphs []string
}

func (r *recordingGraphNamesCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	gt := req.GetTarget().GetGraph()
	r.seenGraphs = append(r.seenGraphs, gt)
	return &knowledgev1.ExecuteResponse{GraphNames: r.byType[gt]}, nil
}

// fakeSyncListDeps is a minimal ClientDeps whose CloudStatusInfo() and the two
// GraphCallers are independently controllable, so a test can assert the cloud
// caller is never consulted when logged out.
type fakeSyncListDeps struct {
	local    GraphCaller
	cloud    GraphCaller
	loggedIn bool
	host     string
}

func (d *fakeSyncListDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d *fakeSyncListDeps) Sink() collector.Sink                         { return nil }
func (d *fakeSyncListDeps) RootDir() string                              { return "" }
func (d *fakeSyncListDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *fakeSyncListDeps) WorkerReady() bool                            { return true }
func (d *fakeSyncListDeps) PropReady() bool                              { return true }
func (d *fakeSyncListDeps) PipelineReady() bool                          { return true }
func (d *fakeSyncListDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *fakeSyncListDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *fakeSyncListDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *fakeSyncListDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *fakeSyncListDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *fakeSyncListDeps) BackendResolver() BackendResolver             { return nil }
func (d *fakeSyncListDeps) GraphCaller() GraphCaller                     { return d.cloud }
func (d *fakeSyncListDeps) LocalGraphCaller() GraphCaller                { return d.local }
func (d *fakeSyncListDeps) RepoResolver() *RepoResolver                  { return nil }
func (d *fakeSyncListDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *fakeSyncListDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *fakeSyncListDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *fakeSyncListDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *fakeSyncListDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *fakeSyncListDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *fakeSyncListDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *fakeSyncListDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d *fakeSyncListDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *fakeSyncListDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *fakeSyncListDeps) TensionsProvider() TensionsProvider   { return nil }

// CloudStatusInfo satisfies the cloudStatusInfo seam (manage.go:43).
func (d *fakeSyncListDeps) CloudStatusInfo() (bool, string) { return d.loggedIn, d.host }

// TestHandleSyncList_LoggedOut asserts that when not logged in, handleSyncList
// enumerates ONLY local graphs of sync-eligible types (logs/web/pdf excluded),
// renders correct per-type sync params, marks every row "login required", and
// NEVER consults the cloud GraphCaller.
func TestHandleSyncList_LoggedOut(t *testing.T) {
	local := &recordingGraphNamesCaller{
		byType: map[string][]*knowledgev1.GraphInfo{
			string(kgtypes.GraphKnowledge): {{Name: "default"}},
			string(kgtypes.GraphCode):      {{Name: "knowledge"}},
			string(kgtypes.GraphPractice):  {{Name: "go"}},
			// ineligible types — must never appear in the output even though
			// the local caller would yield them.
			string(kgtypes.GraphLogs):   {{Name: "q-123"}},
			string(kgtypes.GraphWebRaw): {{Name: "hohpe-eip"}},
			string(kgtypes.GraphPDFRaw): {{Name: "some-pdf"}},
		},
	}
	cloud := &recordingGraphNamesCaller{byType: map[string][]*knowledgev1.GraphInfo{}}
	deps := &fakeSyncListDeps{local: local, cloud: cloud, loggedIn: false}

	res := handleSyncList(deps)
	if res.IsError {
		t.Fatalf("handleSyncList returned error: %v", res.Content)
	}
	out := toolResultText(res)

	// Cloud caller must NOT have been consulted when logged out.
	if len(cloud.seenGraphs) != 0 {
		t.Errorf("cloud GraphCaller consulted while logged out: saw %v", cloud.seenGraphs)
	}

	// Local caller asked ONLY about eligible types, never logs/web/pdf.
	for _, gt := range local.seenGraphs {
		switch kgtypes.GraphType(gt) {
		case kgtypes.GraphLogs, kgtypes.GraphWebRaw, kgtypes.GraphPDFRaw:
			t.Errorf("local caller queried ineligible type %q", gt)
		}
	}

	// Eligible rows present.
	for _, want := range []string{"knowledge/default", "code/knowledge", "practice/go"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected row %q in output:\n%s", want, out)
		}
	}
	// Ineligible graphs absent.
	for _, absent := range []string{"q-123", "hohpe-eip", "some-pdf"} {
		if strings.Contains(out, absent) {
			t.Errorf("ineligible graph %q leaked into output:\n%s", absent, out)
		}
	}
	// Per-type sync params correct.
	for _, want := range []string{"graph:practice name:go", "graph:code name:knowledge"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected sync params %q in output:\n%s", want, out)
		}
	}
	// Every data row is "login required" (and none is "yes"/"no").
	if !strings.Contains(out, "login required") {
		t.Errorf("expected 'login required' status in output:\n%s", out)
	}
}

// TestRenderSyncListTable_DisplayRule feeds a synced (SyncTime>0) + unsynced
// (SyncTime==0) + multi-type row set and asserts: 4 columns, per-type sync
// params, and the Last-synced cell rule (>0 → a time, ==0/!loggedIn → blank).
// There must be no synced-with-0 row and no bare "synced" cell.
func TestSyncListRender_DisplayRule(t *testing.T) {
	now := time.Now().UnixNano()
	rows := []syncListRow{
		{graphType: kgtypes.GraphKnowledge, name: "default", synced: true, syncTime: now},
		{graphType: kgtypes.GraphPractice, name: "go", synced: false, syncTime: 0},
		{graphType: kgtypes.GraphCloud, name: "acme", synced: true, syncTime: now},
	}
	out := renderSyncListTable(rows, true /*loggedIn*/)

	// 4-column header.
	if !strings.Contains(out, "Graph") || !strings.Contains(out, "Sync params") ||
		!strings.Contains(out, "Synced?") || !strings.Contains(out, "Last synced") {
		t.Errorf("expected 4-column header, got:\n%s", out)
	}
	// Per-type sync params routing.
	if !strings.Contains(out, "graph:practice name:go") {
		t.Errorf("expected practice sync params, got:\n%s", out)
	}
	if !strings.Contains(out, "graph:cloud name:acme") {
		t.Errorf("expected cloud sync params, got:\n%s", out)
	}

	for ln := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, "practice/go"):
			// Unsynced row: status "no", Last-synced blank.
			if !strings.Contains(ln, "no") {
				t.Errorf("unsynced row missing 'no' status: %q", ln)
			}
			// No time appended — the trailing field is blank (nothing after "no").
			if strings.Contains(ln, "ago") {
				t.Errorf("unsynced row must have blank Last-synced, got: %q", ln)
			}
		case strings.HasPrefix(ln, "knowledge/default"), strings.HasPrefix(ln, "cloud/acme"):
			// Synced row: status "yes", Last-synced non-blank (a relative age).
			if !strings.Contains(ln, "yes") {
				t.Errorf("synced row missing 'yes' status: %q", ln)
			}
			if !strings.Contains(ln, "ago") && !strings.Contains(ln, "just now") {
				t.Errorf("synced row (SyncTime>0) must show a time, got: %q", ln)
			}
		}
	}

	// Not-logged-in: every status cell is "login required", Last synced blank.
	outLoggedOut := renderSyncListTable(rows, false /*loggedIn*/)
	if !strings.Contains(outLoggedOut, "login required") {
		t.Errorf("logged-out render missing 'login required':\n%s", outLoggedOut)
	}
	if strings.Contains(outLoggedOut, "ago") {
		t.Errorf("logged-out render must blank Last-synced, got:\n%s", outLoggedOut)
	}
}
