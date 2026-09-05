// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// countingStatsRPC is a statsRPC whose Execute calls are COUNTED, which is what
// makes the one-Execute perf leg assertable at all. root is the single node the
// Execute answers with; nil root answers with no nodes, which is the shape an
// empty or unresolvable graph presents.
type countingStatsRPC struct {
	root      *knowledgev1.Node
	execCalls int
}

func (f *countingStatsRPC) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{
		NodeCount: 3,
		EdgeCount: 2,
	}}, nil
}

func (f *countingStatsRPC) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execCalls++
	if f.root == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{f.root}}, nil
}

// TestGraphStatsBody_SurfacesCollectorSchemaVersion covers the three things the
// web/pdf stats read owes: it renders the collected graph's version, it renders
// an EXPLICIT unstamped line when the key is absent rather than dropping the
// line, and it resolves the value in exactly ONE Execute.
//
// The absence case is the one that matters most — a silently missing line
// reproduces the very defect the stamp exists to end, where a reader of a
// pre-versioning graph cannot tell.
func TestGraphStatsBody_SurfacesCollectorSchemaVersion(t *testing.T) {
	sel := &knowledgev1.GraphSelector{Graph: "pdf", Name: "some-book"}

	t.Run("renders_the_version", func(t *testing.T) {
		gc := &countingStatsRPC{root: &knowledgev1.Node{
			Id: "doc1", Type: "document",
			Metadata: map[string]string{"collector_schema_version": "7"},
		}}
		extra := map[string]string{
			"collector_schema_version": rawGraphRootStamps(context.Background(), statsExecOf(gc), sel, "document").schemaVersion,
		}
		text := renderGraphStatsBody(context.Background(), gc, sel, "## PDF Graph: some-book",
			queryArgs{}, extra)
		body := text.Content[0].Text
		if !strings.Contains(body, "collector_schema_version: 7") {
			t.Errorf("text arm did not render the version:\n%s", body)
		}

		gcJSON := &countingStatsRPC{root: gc.root}
		extraJSON := map[string]string{
			"collector_schema_version": rawGraphRootStamps(context.Background(), statsExecOf(gcJSON), sel, "document").schemaVersion,
		}
		jsonOut := renderGraphStatsBody(context.Background(), gcJSON, sel, "## PDF Graph: some-book",
			queryArgs{Format: "json"}, extraJSON).Content[0].Text
		if !strings.Contains(jsonOut, `"collector_schema_version":"7"`) {
			t.Errorf("json arm did not carry the version:\n%s", jsonOut)
		}
	})

	t.Run("renders_unstamped_when_absent", func(t *testing.T) {
		gc := &countingStatsRPC{root: &knowledgev1.Node{
			Id: "doc1", Type: "document",
			Metadata: map[string]string{"source": "pdf"},
		}}
		extra := map[string]string{
			"collector_schema_version": rawGraphRootStamps(context.Background(), statsExecOf(gc), sel, "document").schemaVersion,
		}
		body := renderGraphStatsBody(context.Background(), gc, sel, "## PDF Graph: some-book",
			queryArgs{}, extra).Content[0].Text
		if !strings.Contains(body, "collector_schema_version: unstamped") {
			t.Errorf("an absent key must render an explicit unstamped line, not be omitted:\n%s", body)
		}
		// Known-positive control on the SAME instrument: the stamped root above
		// renders a value through this identical path, so the unstamped string
		// is the graph speaking rather than a line printed unconditionally.
		stamped := &countingStatsRPC{root: &knowledgev1.Node{
			Id: "doc1", Type: "document",
			Metadata: map[string]string{"collector_schema_version": "1"},
		}}
		if got := rawGraphRootStamps(context.Background(), statsExecOf(stamped), sel, "document").schemaVersion; got != "1" {
			t.Fatalf("control: a stamped root resolved to %q, want \"1\" — the read path is broken, not the graph", got)
		}
	})

	t.Run("resolves_in_one_execute", func(t *testing.T) {
		gc := &countingStatsRPC{root: &knowledgev1.Node{
			Id: "p1", Type: "page",
			Metadata: map[string]string{"collector_schema_version": "1"},
		}}
		webSel := &knowledgev1.GraphSelector{Graph: "web", Name: "some-site"}
		extra := map[string]string{
			"collector_schema_version": rawGraphRootStamps(context.Background(), statsExecOf(gc), webSel, "page").schemaVersion,
		}
		// Samples false: a samples render issues one further Execute per node
		// type through fetchGraphSamples, which would swamp this count.
		renderGraphStatsBody(context.Background(), gc, webSel, "## Web Graph: some-site",
			queryArgs{}, extra)
		if gc.execCalls != 1 {
			t.Errorf("version resolution issued %d Execute calls, want exactly 1 — a per-node-type loop is the shape this rejects",
				gc.execCalls)
		}
	})
}
