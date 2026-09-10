// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// style_rule_import_fake_test.go holds the store fake the import rows drive. It
// is a sibling of style_rule_import_test.go, which sits against the repo's
// per-file length ceiling, on the same precedent every other harness split in
// this package follows: the SEED is shared, and a reader looking for what a row
// asserts should not scroll past it.

type styleRuleStoreFake struct {
	nodes    map[string]*knowledgev1.Node
	order    []string
	plans    []*knowledgev1.MutationPlan
	targets  []string
	edges    []*knowledgev1.BatchEdgeSpec
	nextID   int
	failNext bool
}

func newStyleRuleStoreFake() *styleRuleStoreFake {
	// THE HUB EVERY IMPORT NAMES IS SEEDED, because the write path resolves it:
	// a `source_hub` must name a live `source` node before anything is written
	// under it, and a fake that held only the rules would report that refusal
	// instead of the import behaviour these rows measure. A hub carries its OWN
	// id under the hub key, which is the hub contract.
	f := &styleRuleStoreFake{nodes: map[string]*knowledgev1.Node{}}
	hub := &knowledgev1.Node{
		Id: "hub-go", Type: string(kgtypes.NodeSource),
		Metadata: map[string]string{kgtypes.MetaKeySourceHub: "hub-go"},
	}
	f.nodes[hub.GetId()] = hub
	f.order = append(f.order, hub.GetId())
	return f
}

func (f *styleRuleStoreFake) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		return f.mutate(m, req.GetTarget().GetGraph())
	}
	return f.read(req.GetQuery()), nil
}

func (f *styleRuleStoreFake) read(plan *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	sel := plan.GetSelection()
	var out []*knowledgev1.Node
	for _, id := range f.order {
		n := f.nodes[id]
		if !styleIndexFakeIDMatch(styleFakeReadIDs(plan), id) {
			continue
		}
		if !styleIndexFakePredMatch(sel.GetMetadataPredicates(), n.GetMetadata()) {
			continue
		}
		out = append(out, n)
	}
	return &knowledgev1.ExecuteResponse{
		Nodes: styleIndexFakePage(out, int(plan.GetOffset()), int(plan.GetLimit())),
		Total: int64(len(out)),
	}
}

func (f *styleRuleStoreFake) mutate(
	m *knowledgev1.MutationPlan, graph string,
) (*knowledgev1.ExecuteResponse, error) {
	f.plans = append(f.plans, m)
	f.targets = append(f.targets, graph)
	f.edges = append(f.edges, m.GetEdges()...)
	if f.failNext {
		f.failNext = false
		return nil, assertStoreUnavailable{}
	}
	switch m.GetKind() {
	case knowledgev1.MutationPlan_MUTATION_KIND_CREATE:
		return f.create(m), nil
	case knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS:
		return f.updateItems(m), nil
	case knowledgev1.MutationPlan_MUTATION_KIND_DELETE:
		return f.deleteMatching(m), nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// create stores each body WHOLE — the store's own behaviour, and the reason a
// re-create over an existing node clears the fields the payload omits.
func (f *styleRuleStoreFake) create(m *knowledgev1.MutationPlan) *knowledgev1.ExecuteResponse {
	ids := make([]string, 0, len(m.GetNodeBodies()))
	for _, b := range m.GetNodeBodies() {
		id := b.GetId()
		if id == "" {
			f.nextID++
			id = "generated-rule-" + string(rune('a'+f.nextID-1))
		}
		if _, existed := f.nodes[id]; !existed {
			f.order = append(f.order, id)
		}
		f.nodes[id] = &knowledgev1.Node{
			Id: id, Type: b.GetType(), SymbolName: b.GetName(),
			Summary: b.GetSummary(), Description: b.GetDescription(),
			Metadata: b.GetMetadata(),
		}
		ids = append(ids, id)
	}
	return &knowledgev1.ExecuteResponse{Ids: ids, AffectedCount: int64(len(ids))}
}

// updateItems copies only the fields the item NAMES and merges metadata PER KEY.
func (f *styleRuleStoreFake) updateItems(m *knowledgev1.MutationPlan) *knowledgev1.ExecuteResponse {
	n := 0
	for _, it := range m.GetUpdateItems() {
		node, ok := f.nodes[it.GetId()]
		if !ok {
			continue
		}
		if it.Summary != nil {
			node.Summary = it.GetSummary()
		}
		if it.Description != nil {
			node.Description = it.GetDescription()
		}
		maps.Copy(node.Metadata, it.GetMetadata())
		n++
	}
	return &knowledgev1.ExecuteResponse{AffectedCount: int64(n)}
}

type assertStoreUnavailable struct{}

func (assertStoreUnavailable) Error() string { return "store unavailable" }

// writeRuleList writes a rule-list file into the test's own temp directory and
// returns its absolute path.
func writeRuleList(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rules.json")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// importRules drives the real manage dispatch arm.
func importRules(t *testing.T, f *styleRuleStoreFake, body string, dryRun bool) (string, bool) {
	t.Helper()
	res := handleImportStyleRules(opCtx(), interceptTestDeps{gc: f}, manageArgs{
		Operation: OpStyleRulesImport,
		Source:    "hub-go",
		Path:      writeRuleList(t, body),
		DryRun:    dryRun,
	})
	return toolResultText(res), res.IsError
}

// oneRule is a minimal well-formed list, used as the known-positive control for
// every refusal below: without it, a parser that refused everything would
// satisfy each refusal assertion on its own.
const oneRule = `{"rules":[{"name":"no-bare-http-client","summary":"never use http.DefaultClient",` +
	`"text":"Construct an http.Client with an explicit timeout.","severity":"warning"}]}`
