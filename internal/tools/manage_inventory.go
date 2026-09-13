// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// inventoryRouter supplies separate, account-bound reads without federating names.
type inventoryRouter interface {
	InventoryContexts(context.Context) ([]context.Context, error)
	InventoryPlacement(context.Context) (string, string, error)
}
type inventoryRow struct {
	Family    string            `json:"family"`
	Name      string            `json:"name"`
	Storage   string            `json:"storage"`
	AccountID string            `json:"account,omitempty"`
	Selector  map[string]string `json:"selector"`
	Nodes     *int64            `json:"nodes"`
	Edges     *int64            `json:"edges"`
}

func handleGraphInventory(ctx context.Context, deps ClientDeps) kgtools.ToolResult {
	rows, err := graphInventory(ctx, deps)
	if err != nil {
		return errorResult("graph_inventory: " + err.Error() + "; check backend access and retry")
	}
	return jsonResult(map[string]any{"graphs": rows})
}

// inventoryPlacement is presentation metadata for one already-bound context.
type inventoryPlacement struct {
	storage, account string
	routed           bool
}
type inventoryGraph struct {
	family kgtypes.GraphType
	name   string
	target *knowledgev1.GraphSelector
}

func graphInventory(ctx context.Context, deps ClientDeps) ([]inventoryRow, error) {
	gc := deps.GraphCaller()
	sc, ok := gc.(statsRPC)
	if !ok {
		return nil, fmt.Errorf("graph statistics are unavailable")
	}
	contexts := []context.Context{ctx}
	router, routed := gc.(inventoryRouter)
	if routed {
		var err error
		contexts, err = router.InventoryContexts(ctx)
		if err != nil {
			return nil, err
		}
	}
	// Destinations are independent; one shared budget bounds catalog and count RPCs.
	sem := make(chan struct{}, coverageStatsConcurrency)
	batches := make([][]inventoryRow, len(contexts))
	errs := make([]error, len(contexts))
	var wg sync.WaitGroup
	for i, readCtx := range contexts {
		wg.Go(func() {
			place, err := graphInventoryPlacement(readCtx, deps, router, routed)
			if err != nil {
				errs[i] = err
				return
			}
			graphs, err := graphInventoryTargets(readCtx, deps, sem)
			if err != nil {
				errs[i] = err
				return
			}
			batches[i], errs[i] = graphInventoryCounts(readCtx, sc, graphs, place, sem)
		})
	}
	wg.Wait()
	rows := make([]inventoryRow, 0)
	for i, batch := range batches {
		if errs[i] != nil {
			return nil, errs[i]
		}
		rows = append(rows, batch...)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		return a.Storage+"\x00"+a.AccountID+"\x00"+a.Family+"\x00"+a.Name < b.Storage+"\x00"+b.AccountID+"\x00"+b.Family+"\x00"+b.Name
	})
	return rows, nil
}
func graphInventoryPlacement(ctx context.Context, deps ClientDeps, router inventoryRouter, routed bool) (inventoryPlacement, error) {
	place := inventoryPlacement{storage: "local", routed: routed}
	if routed {
		var err error
		place.storage, place.account, err = router.InventoryPlacement(ctx)
		return place, err
	}
	if status, ok := deps.(cloudStatusInfo); ok {
		if loggedIn, _ := status.CloudStatusInfo(); loggedIn {
			place.storage = "cloud"
		}
	}
	return place, nil
}

// sem is bidirectional: sending acquires an RPC slot and receiving releases it.
func graphInventoryTargets(ctx context.Context, deps ClientDeps, sem chan struct{}) ([]inventoryGraph, error) {
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	families, err := coverageWalkTypes(ctx, deps)
	<-sem
	if err != nil {
		return nil, err
	}
	batches := make([][]inventoryGraph, len(families))
	errs := make([]error, len(families))
	var wg sync.WaitGroup
	// Families are independent; each catalog must precede its dependent overlays.
	for i, family := range families {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			infos, err := fetchGraphNamesOfType(ctx, deps.GraphCaller(), string(family.gt))
			if err != nil {
				errs[i] = err
				return
			}
			for _, info := range infos {
				name := info.GetName()
				batches[i] = append(batches[i], inventoryGraph{family: family.gt, name: name, target: statusGraphTarget(family.gt, name)})
				if family.gt != kgtypes.GraphCode {
					continue
				}
				overlays, err := graphInventoryOverlays(ctx, deps, name)
				if err != nil {
					errs[i] = err
					return
				}
				batches[i] = append(batches[i], overlays...)
			}
		})
	}
	wg.Wait()
	var graphs []inventoryGraph
	for i, batch := range batches {
		if errs[i] != nil {
			return nil, errs[i]
		}
		graphs = append(graphs, batch...)
	}
	return graphs, nil
}
func graphInventoryOverlays(ctx context.Context, deps ClientDeps, name string) ([]inventoryGraph, error) {
	keys, err := listOverlayKeysOfBase(ctx, deps, "code", name)
	if err != nil {
		return nil, err
	}
	var graphs []inventoryGraph
	for _, key := range keys {
		branch := bareOverlayName(name, key)
		if branch == "" {
			continue
		}
		fullName := name + "@" + branch
		target := statusGraphTarget(kgtypes.GraphCode, name)
		target.Branch = branch
		graphs = append(graphs, inventoryGraph{family: kgtypes.GraphCode, name: fullName, target: target})
	}
	return graphs, nil
}
func graphInventoryRow(graph inventoryGraph, place inventoryPlacement) inventoryRow {
	target := graph.target
	selector := map[string]string{"graph": string(graph.family)}
	if place.routed {
		selector["storage"] = place.storage
	}
	if place.account != "" {
		selector["account"] = place.account
	}
	for key, value := range map[string]string{"repo": target.GetRepo(), "language": target.GetLanguage(), "branch": target.GetBranch()} {
		if value != "" {
			selector[key] = value
		}
	}
	// Knowledge and linkage query intercepts address singletons and refuse a name.
	if target.GetName() != "" && graph.family != kgtypes.GraphKnowledge && graph.family != kgtypes.GraphLinkage {
		selector["name"] = target.GetName()
	}
	return inventoryRow{Family: string(graph.family), Name: graph.name, Storage: place.storage, AccountID: place.account, Selector: selector}
}

// sem is bidirectional: sending acquires an RPC slot and receiving releases it.
func graphInventoryCounts(ctx context.Context, sc statsRPC, graphs []inventoryGraph, place inventoryPlacement, sem chan struct{}) ([]inventoryRow, error) {
	rows := make([]inventoryRow, len(graphs))
	errs := make([]error, len(graphs))
	var wg sync.WaitGroup
	for i, graph := range graphs {
		wg.Go(func() {
			rows[i] = graphInventoryRow(graph, place)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			response, err := sc.Stats(ctx, &knowledgev1.StatsRequest{Target: graph.target})
			if err != nil {
				errs[i] = err
				return
			}
			if stats := response.GetGraphStats(); stats != nil {
				nodes, edges := int64(stats.GetNodeCount()), int64(stats.GetEdgeCount())
				rows[i].Nodes = &nodes
				rows[i].Edges = &edges
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("%s graph counts: %w", place.storage, err)
		}
	}
	return rows, nil
}
