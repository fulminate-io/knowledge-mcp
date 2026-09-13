// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

func TestGraphInventory(t *testing.T) {
	deps := &coverageDeps{gc: &coverageFake{baseNamesByType: map[string][]string{"code": {"fixture"}}}}
	handled, result := InterceptManage(t.Context(), deps, kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(`{"operation":"graph_inventory","format":"json"}`)})
	if !handled || result.IsError {
		t.Fatalf("inventory: handled=%v result=%s", handled, toolResultText(result))
	}
	var body struct{ Graphs []inventoryRow }
	if err := json.Unmarshal([]byte(toolResultText(result)), &body); err != nil {
		t.Fatalf("graphInventory: %v", err)
	}
	found := false
	for _, row := range body.Graphs {
		if row.Storage != "local" {
			t.Errorf("storage=%q, want local", row.Storage)
		}
		if row.Family == "code" && row.Name == "fixture" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fixture absent from inventory: %+v", body.Graphs)
	}
}
func TestGraphInventoryReadFailures(t *testing.T) {
	for _, kind := range []string{"catalog denied", "catalog unreachable", "family catalog", "overlay catalog", "partial count"} {
		t.Run(kind, func(t *testing.T) {
			fixture := &inventoryFailureFixture{coverageFake: &coverageFake{baseNamesByType: map[string][]string{"code": {"fixture"}}}, kind: kind}
			rows, err := graphInventory(t.Context(), &coverageDeps{gc: fixture})
			if err == nil || rows != nil {
				t.Fatalf("failure must not yield complete rows: rows=%v error=%v", rows, err)
			}
			fixture.kind = ""
			rows, err = graphInventory(t.Context(), &coverageDeps{gc: fixture})
			if err != nil || len(rows) == 0 {
				t.Fatalf("successful control: rows=%v error=%v", rows, err)
			}
		})
	}
}

type inventoryFailureFixture struct {
	*coverageFake
	kind string
}

func (f *inventoryFailureFixture) Execute(ctx context.Context, request *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if f.kind == "catalog denied" || f.kind == "catalog unreachable" {
		return nil, fmt.Errorf("%s", f.kind)
	}
	if f.kind == "family catalog" && request.GetTarget().GetGraph() == "code" || f.kind == "overlay catalog" && request.GetQuery().GetOverlayOf() != "" {
		return nil, fmt.Errorf("%s", f.kind)
	}
	return f.coverageFake.Execute(ctx, request)
}
func (f *inventoryFailureFixture) Stats(ctx context.Context, request *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	if f.kind == "partial count" && request.GetTarget().GetRepo() == "fixture" {
		return nil, fmt.Errorf("fixture count unavailable")
	}
	return f.coverageFake.Stats(ctx, request)
}
func TestGraphInventoryEmpty(t *testing.T) {
	rows, err := graphInventory(t.Context(), &coverageDeps{gc: &coverageFake{baseNamesByType: map[string][]string{"code": {}, "practice": {}}}})
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty inventory: rows=%v error=%v", rows, err)
	}
}

type inventoryStorageKey struct{}
type inventoryStorageFixture struct{ *coverageFake }

func (f *inventoryStorageFixture) InventoryContexts(ctx context.Context) ([]context.Context, error) {
	return []context.Context{context.WithValue(ctx, inventoryStorageKey{}, "local"), context.WithValue(ctx, inventoryStorageKey{}, "cloud")}, nil
}
func (f *inventoryStorageFixture) InventoryPlacement(ctx context.Context) (string, string, error) {
	storage := ctx.Value(inventoryStorageKey{}).(string)
	if storage == "cloud" {
		return storage, "fixture-account", nil
	}
	return storage, "", nil
}
func (f *inventoryStorageFixture) Stats(ctx context.Context, request *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	count := int32(3)
	if ctx.Value(inventoryStorageKey{}) == "cloud" {
		count = 7
	}
	return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{NodeCount: count}}, nil
}
func TestGraphInventoryPlacement(t *testing.T) {
	fixture := &inventoryStorageFixture{coverageFake: &coverageFake{baseNamesByType: map[string][]string{"code": {"same-name"}, "practice": {}}}}
	rows, err := graphInventory(t.Context(), &coverageDeps{gc: fixture})
	if err != nil {
		t.Fatalf("graphInventory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("graphInventory() = %+v, want local and cloud copies", rows)
	}
	for _, row := range rows {
		want := int64(3)
		if row.Storage == "cloud" {
			want = 7
			if row.AccountID != "fixture-account" || row.Selector["account"] != "fixture-account" {
				t.Fatalf("cloud account lost: %+v", row)
			}
		}
		if row.Nodes == nil || *row.Nodes != want || row.Selector["storage"] != row.Storage || row.Selector["repo"] != "same-name" {
			t.Fatalf("placement or selector lost: %+v", row)
		}
	}
}

func TestGraphInventoryOverlaySelector(t *testing.T) {
	fixture := &coverageFake{baseNamesByType: map[string][]string{"code": {"fixture"}, "practice": {}}, overlayKeysByBase: map[string][]string{"fixture": {"feature/topic"}}}
	rows, err := graphInventory(t.Context(), &coverageDeps{gc: fixture})
	if err != nil {
		t.Fatalf("graphInventory: %v", err)
	}
	for _, row := range rows {
		if row.Name == "fixture@feature/topic" {
			if row.Selector["repo"] != "fixture" || row.Selector["branch"] != "feature/topic" {
				t.Fatalf("overlay selector=%v", row.Selector)
			}
			return
		}
	}
	t.Fatal("graphInventory: overlay missing")
}

type inventoryConcurrentFixture struct {
	*inventoryStorageFixture
	entered chan string
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (f *inventoryConcurrentFixture) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES && req.GetTarget().GetGraph() != "" && req.GetQuery().GetOverlayOf() == "" {
		active := f.active.Add(1)
		defer f.active.Add(-1)
		for peak := f.peak.Load(); active > peak; peak = f.peak.Load() {
			if f.peak.CompareAndSwap(peak, active) {
				break
			}
		}
		f.entered <- fmt.Sprint(ctx.Value(inventoryStorageKey{})) + ":" + req.GetTarget().GetGraph()
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.coverageFake.Execute(ctx, req)
}
func TestGraphInventoryIndependentReadsBounded(t *testing.T) {
	fixture := &inventoryConcurrentFixture{inventoryStorageFixture: &inventoryStorageFixture{coverageFake: &coverageFake{}}, entered: make(chan string, 64), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := graphInventory(ctx, &coverageDeps{gc: fixture}); done <- err }()
	var reads []string
	for range coverageStatsConcurrency {
		select {
		case name := <-fixture.entered:
			reads = append(reads, name)
		case <-ctx.Done():
			close(fixture.release)
			<-done
			t.Fatalf("independent reads remained serialized: %v", reads)
		}
	}
	close(fixture.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak := fixture.peak.Load(); peak > coverageStatsConcurrency {
		t.Fatalf("catalog concurrency=%d exceeds %d", peak, coverageStatsConcurrency)
	}
	local, cloud := false, false
	for _, read := range reads {
		local = local || strings.HasPrefix(read, "local:")
		cloud = cloud || strings.HasPrefix(read, "cloud:")
	}
	if !local || !cloud {
		t.Fatalf("destinations did not overlap: %v", reads)
	}
}
