// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// rawModulesSyncTime is the value every seeded CATALOG row carries under
// GraphInfo.SyncTime, and that NO root node carries anywhere. It is the
// discriminator for the two-stamper defect: an implementation that renders the
// registry timestamp as the collect time emits this number, and an
// implementation that reads the root emits an RFC3339 instant. A value no root
// holds is what makes "the registry timestamp renders nowhere" measurable
// rather than inferred.
const rawModulesSyncTime int64 = 1700000000000000000

// rawModulesDrainSeed is how many nodes each seeded graph holds. It is over
// paging.BrowsePageSize so a drain-based implementation pages more than once
// per graph — a fixture holding fewer nodes than one page would let a drain
// look identical to a bounded root read.
const rawModulesDrainSeed = 600

// rawModulesFake answers the two read shapes the listing issues and counts them
// apart: the catalog enumeration (RETURN_MODE_GRAPH_NAMES) and the per-graph
// root read. A third counter identifies DRAIN pages structurally — any read
// carrying a keyset AfterId cursor, or a match-all browse with a page-sized
// Limit — so the perf assertion names the shape it rejects rather than a total
// that a different implementation could coincidentally match.
type rawModulesFake struct {
	catalog    []*knowledgev1.GraphInfo
	catalogErr error
	// roots maps a graph name to the root node its Limit-1 read answers with.
	// A name absent from the map answers with no nodes, which is the shape an
	// empty or unresolvable graph presents.
	roots map[string]*knowledgev1.Node
	// bulk maps a graph name to the full node list a DRAIN would page through.
	// Only a drain-shaped read reaches it.
	bulk map[string][]*knowledgev1.Node

	catalogReads int
	rootReads    int
	drainPages   int
	totalExecs   int
	rootTargets  []string
}

func (f *rawModulesFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.totalExecs++
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		f.catalogReads++
		if f.catalogErr != nil {
			return nil, f.catalogErr
		}
		return &knowledgev1.ExecuteResponse{GraphNames: f.catalog}, nil
	}
	name := req.GetTarget().GetName()
	// A drain page, identified STRUCTURALLY rather than by count: the keyset
	// browse sets AfterId on every page including the first, and it selects
	// every node type at once with a page-sized Limit.
	if q.AfterId != nil || (q.GetSelection().GetNodeType() == "" && q.GetLimit() >= int32(paging.BrowsePageSize)) {
		f.drainPages++
		return &knowledgev1.ExecuteResponse{Nodes: f.drainPage(name, q.GetAfterId())}, nil
	}
	f.rootReads++
	f.rootTargets = append(f.rootTargets, name)
	if n := f.roots[name]; n != nil {
		return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{n}}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// drainPage serves one keyset page of a graph's seeded bulk, so a drain-based
// implementation pages realistically instead of terminating on the first read.
func (f *rawModulesFake) drainPage(name, afterID string) []*knowledgev1.Node {
	all := f.bulk[name]
	start := 0
	if afterID != "" {
		for i, n := range all {
			if n.GetId() == afterID {
				start = i + 1
				break
			}
		}
	}
	end := min(start+paging.BrowsePageSize, len(all))
	if start >= end {
		return nil
	}
	return all[start:end]
}

// rawModulesFixture seeds the three-graph catalog every modules test reads:
// two graphs stamped with DIFFERENT collect instants and DIFFERENT schema
// versions, and one collected before stamping existed. Every catalog row
// carries rawModulesSyncTime; no root does.
func rawModulesFixture(rootType string) *rawModulesFake {
	f := &rawModulesFake{
		roots: map[string]*knowledgev1.Node{
			"alpha-site": {Id: "r-alpha", Type: rootType, Metadata: map[string]string{
				"collected_at": "2026-09-01T10:00:00Z", "collector_schema_version": "1",
			}},
			"beta-site": {Id: "r-beta", Type: rootType, Metadata: map[string]string{
				"collected_at": "2026-08-14T22:30:00Z", "collector_schema_version": "3",
			}},
			// Collected before either key existed: both stamps absent.
			"legacy-site": {Id: "r-legacy", Type: rootType, Metadata: map[string]string{
				"url": "https://example.com/legacy",
			}},
		},
		bulk: map[string][]*knowledgev1.Node{},
	}
	for _, name := range []string{"alpha-site", "beta-site", "legacy-site"} {
		f.catalog = append(f.catalog, &knowledgev1.GraphInfo{
			Name: name, Loaded: true, SyncTime: rawModulesSyncTime,
		})
		// The root leads the bulk, so a DRAIN-based implementation finds the
		// same stamps and renders byte-identical output. That is the point: it
		// makes the perf gate the only thing standing between the bounded
		// implementation and the one whose cost scales with document size.
		nodes := []*knowledgev1.Node{f.roots[name]}
		for i := range rawModulesDrainSeed {
			nodes = append(nodes, &knowledgev1.Node{
				Id: fmt.Sprintf("%s-n%04d", name, i), Type: "paragraph", Content: "body",
			})
		}
		f.bulk[name] = nodes
	}
	return f
}

// TestWebPDFModules_ReportsPerGraphCollectStamp is the correctness gate on the
// raw modules listing.
//
// LOGICAL DEFECT CLASSES IT DETECTS, none of which the perf sibling notices:
// the TWO-STAMPER defect (rendering GraphInfo.SyncTime, written by the registry
// rather than by the collect, as the collect time); the DROPPED-LINE defect
// (omitting the line when the key is absent, which reproduces the exact
// illegibility the stamp exists to end); and the ONE-STAMP-FOR-ALL defect
// (reading one root and reusing its values across every row).
func TestWebPDFModules_ReportsPerGraphCollectStamp(t *testing.T) {
	for _, tc := range []struct{ graph, rootType string }{
		{"web", "page"},
		{"pdf", "document"},
	} {
		t.Run(tc.graph, func(t *testing.T) {
			fake := rawModulesFixture(tc.rootType)
			handled, res := gatedRouteWebPDF(opCtx(), interceptTestDeps{gc: fake},
				queryArgs{Graph: tc.graph, Mode: "modules"})
			require.True(t, handled, "%s mode:modules must be claimed by the raw arm", tc.graph)
			require.False(t, res.IsError, "listing errored: %s", res.Content[0].Text)
			body := res.Content[0].Text

			// Each stamped graph renders ITS OWN instant. Two different values
			// is what separates a per-graph read from one root reused for all.
			assert.Contains(t, body, "2026-09-01T10:00:00Z", "alpha's own collect instant must render")
			assert.Contains(t, body, "2026-08-14T22:30:00Z", "beta's own collect instant must render")

			// Both distinct schema versions render, for the same reason.
			assert.Contains(t, body, "collector_schema_version: 1")
			assert.Contains(t, body, "collector_schema_version: 3")

			// The unstamped graph is MARKED, never dropped.
			assert.Contains(t, body, "legacy-site", "an unstamped graph is still listed")
			assert.Contains(t, body, rawGraphUnstampedCollect,
				"an absent collect stamp must render the explicit unstamped sentence, not a dropped line")
			assert.Contains(t, body, rawGraphUnstampedVersion,
				"an absent schema version must render the explicit unstamped sentence")

			// THE REGISTRY TIMESTAMP RENDERS NOWHERE. Every catalog row carries
			// it and no root does, so its absence here is the listing choosing
			// the root over the catalog row rather than the fixture being bare.
			assert.NotContains(t, body, fmt.Sprint(rawModulesSyncTime),
				"GraphInfo.SyncTime is a registry timestamp from a different stamper and must never render as a collect time")

			// Each listed graph's root is read EXACTLY ONCE.
			assert.Equal(t, 3, fake.rootReads, "one root read per listed graph")
			assert.ElementsMatch(t, []string{"alpha-site", "beta-site", "legacy-site"}, fake.rootTargets,
				"each listed graph's own root must be the one read")
		})
	}
}

// TestWebPDFModules_SeparatesCatalogFailureFromEmptyCatalog pins the property
// PAIR on the catalog read's outcome.
//
// BOTH DIRECTIONS ARE ASSERTED because a stub that always errors and a stub
// that always succeeds each satisfy one alone. The pair matters directly: the
// cleanup flow reads an empty listing as evidence a sweep worked, so an
// implementation that rendered a FAILED catalog read as an empty listing would
// make that evidence a lie.
func TestWebPDFModules_SeparatesCatalogFailureFromEmptyCatalog(t *testing.T) {
	t.Run("catalog_read_failure_is_an_error", func(t *testing.T) {
		fake := rawModulesFixture("page")
		fake.catalogErr = fmt.Errorf("backend unreachable")
		handled, res := gatedRouteWebPDF(opCtx(), interceptTestDeps{gc: fake},
			queryArgs{Graph: "web", Mode: "modules"})
		require.True(t, handled)
		require.True(t, res.IsError, "a failed catalog read must be an error, never an empty listing")
		assert.Contains(t, res.Content[0].Text, "backend unreachable", "the error must name the cause")
		assert.Zero(t, fake.rootReads, "a failed catalog read reads no roots")
	})

	t.Run("empty_catalog_is_a_success_naming_zero_graphs", func(t *testing.T) {
		fake := &rawModulesFake{roots: map[string]*knowledgev1.Node{}}
		handled, res := gatedRouteWebPDF(opCtx(), interceptTestDeps{gc: fake},
			queryArgs{Graph: "web", Mode: "modules"})
		require.True(t, handled)
		require.False(t, res.IsError, "an empty catalog is an ordinary answer, not a fault")
		body := res.Content[0].Text
		assert.Contains(t, body, "(0)", "an empty listing names zero graphs")
		assert.Equal(t, 1, fake.catalogReads,
			"control: the catalog WAS read — a zero from a read that never happened proves nothing")
	})
}

// TestWebPDFModules_CostsOneReadPerListedGraph is the performance gate: the
// listing's cost scales with the NUMBER of collected documents, never with
// their size.
//
// The correctness sibling cannot detect this class — a drain-based
// implementation renders byte-identical output. Every graph in the fixture
// holds rawModulesDrainSeed nodes, over one page, so a drain really does page.
//
// DISCRIMINATING CONTROL FOR THE ZERO: the drain-page counter is not asserted
// zero in isolation. The same fake counts root reads and total Executes in the
// same run, and both are asserted NON-ZERO, so a fake that silently counted
// nothing would fail those rather than pass this.
func TestWebPDFModules_CostsOneReadPerListedGraph(t *testing.T) {
	fake := rawModulesFixture("page")
	handled, res := gatedRouteWebPDF(opCtx(), interceptTestDeps{gc: fake},
		queryArgs{Graph: "web", Mode: "modules"})
	require.True(t, handled, "web mode:modules must be claimed by the raw arm")
	require.False(t, res.IsError, "listing errored: %s", res.Content[0].Text)

	listed := len(fake.catalog)
	require.Positive(t, listed, "the fixture seeded no graphs; every bound below would be vacuous")

	assert.Equal(t, 1, fake.catalogReads, "exactly one catalog read")
	assert.LessOrEqual(t, fake.rootReads, listed,
		"at most one root read per listed graph — the ceiling is the seeded catalog length, not a pinned constant")
	assert.LessOrEqual(t, fake.totalExecs, 1+listed,
		"total Executes are bounded by one catalog read plus one root read per listed graph")
	assert.Zero(t, fake.drainPages,
		"the listing must never page a graph's nodes: a raw graph's stamps live on one root node")

	// The known-positives that keep the zero above honest.
	assert.Positive(t, fake.rootReads, "control: roots WERE read")
	assert.Positive(t, fake.totalExecs, "control: Executes WERE counted")
}
