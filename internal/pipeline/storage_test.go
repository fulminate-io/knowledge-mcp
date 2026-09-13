// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

func TestStorageWantedGraphsRetainDestination(t *testing.T) {
	ws := workingset.New()
	ws.AdmitRef(workingset.Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "local"}, "query")
	ws.AdmitRef(workingset.Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "cloud", Account: "a"}, "query")
	p := New(Config{}, nil, nil, nil)
	p.AttachWorkingSet(ws)
	graphs := p.wantedGraphs()
	if len(graphs) != 2 {
		t.Fatalf("wanted=%+v", graphs)
	}
	if graphKey(graphs[0]) == graphKey(graphs[1]) {
		t.Fatal("wanted collectors collapse local/cloud copies")
	}
}

func TestStorageGenerationSnapshotsAreIndependent(t *testing.T) {
	p := New(Config{}, nil, nil, nil)
	local := graphclient.Destination{Storage: "local"}
	cloud := graphclient.Destination{Storage: "cloud", AccountID: "a"}
	p.applyStorageGenPollResponse(local, &knowledgev1.PipelineGenPollResponse{Entries: []*knowledgev1.PipelineGenPollEntry{entry("same", "summary", 9)}})
	p.applyStorageGenPollResponse(cloud, &knowledgev1.PipelineGenPollResponse{Entries: []*knowledgev1.PipelineGenPollEntry{entry("same", "summary", 2)}})
	a, _, _ := p.genSnapshotFor(graphKey{GraphType: genPollGT, GraphName: "same", Destination: local})
	b, _, _ := p.genSnapshotFor(graphKey{GraphType: genPollGT, GraphName: "same", Destination: cloud})
	if a != 9 || b != 2 {
		t.Fatalf("local=%d cloud=%d", a, b)
	}
}

func TestStorageQueuedWorkRetainsDestination(t *testing.T) {
	local := graphclient.Destination{Storage: "local"}
	cloud := graphclient.Destination{Storage: "cloud", AccountID: "a"}
	s := groupSummaryByGraph([]SummaryWork{{NodeID: "same", Destination: local}, {NodeID: "same", Destination: cloud}})
	e := groupEmbedByGraph([]EmbedWork{{NodeID: "same", Destination: local}, {NodeID: "same", Destination: cloud}})
	if len(s) != 2 || len(e) != 2 {
		t.Fatal("queued storage identities collapsed")
	}
}

func TestStorageCatalogGenerationsRemainIndependent(t *testing.T) {
	p := New(Config{}, nil, nil, nil)
	local := graphclient.Destination{Storage: "local"}
	cloud := graphclient.Destination{Storage: "cloud", AccountID: "a"}
	for _, item := range []struct {
		destination graphclient.Destination
		gen         uint64
		moved       bool
	}{{local, 9, false}, {cloud, 2, false}, {local, 9, false}, {cloud, 3, true}, {local, 10, true}} {
		_, _, moved := p.applyStorageGenPollResponse(item.destination, &knowledgev1.PipelineGenPollResponse{CatalogGen: item.gen})
		if moved != item.moved {
			t.Fatalf("destination=%+v gen=%d moved=%v want=%v", item.destination, item.gen, moved, item.moved)
		}
	}
}

func TestStorageSegmentNudgeRetainsDestination(t *testing.T) {
	p := New(Config{}, nil, nil, nil)
	want := graphclient.Destination{Storage: "cloud", AccountID: "a"}
	var got graphclient.Destination
	p.SetStorageSegmentNudger(func(ctx context.Context, _ kgtypes.GraphType, _ string) { got, _ = graphclient.StorageDestination(ctx) })
	p.segmentNudger()(graphclient.WithDestination(t.Context(), want), kgtypes.GraphKnowledge, "default")
	if got != want {
		t.Fatalf("nudge destination=%+v want=%+v", got, want)
	}
}
