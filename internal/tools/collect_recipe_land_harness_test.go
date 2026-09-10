// SPDX-License-Identifier: Apache-2.0

// collect_recipe_land_test.go — the landing run, observed at the artifact the
// server acts on.
//
// EVERY ASSERTION IS AGAINST THE COMPILED MUTATION PLAN OR THE ISSUED READ, never
// against the rendered response alone. A renderer can agree with a wrong plan,
// and the two failure modes this file exists to catch are both invisible in a
// rendered string: a collision read issued against the WRONG GRAPH (which finds
// no resident for any emitted id, never enters the twin branch, and overwrites a
// hand-edited row on an add-not-upsert path) and a truncation verdict DISCARDED
// (which does the same thing to every resident the read did not see). Both
// produce a successful-looking run.
//
// SO THE READS ARE ASSERTED BY SELECTOR. The fake records the (graph, name) each
// Execute was issued against, and the cells for requirements 2, 4, 7 and 8 assert
// those rather than only the branch that was taken.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// ---------------------------------------------------------------------------
// The fake
// ---------------------------------------------------------------------------

// landingRead records one Execute the landing issued, with the selector it named.
type landingRead struct {
	graph    string
	name     string
	language string
	ids      []string
	nodeType string
	meta     map[string]string
}

// landingCaller serves the raw source graph and the combined practice graph, and
// records every read's SELECTOR alongside its plan.
//
// IT SERVES PRACTICE BY ID AND BY TYPE+META SEPARATELY because the landing issues
// two structurally different reads against it — a by-ids collision hydrate and a
// by-type hub probe — and conflating them would let a landing that issued only
// one of them pass.
type landingCaller struct {
	sourceNodes []*knowledgev1.Node

	// practiceByID is the resident population the collision read hydrates.
	practiceByID map[string]*knowledgev1.Node
	// practiceHubs is what the hub probe returns.
	practiceHubs []*knowledgev1.Node

	// practiceAbsent makes every practice read answer connect.CodeNotFound, which
	// is what the server does for a graph that does not exist
	// (TestExecute_PracticeReadNeverCreates in the bootstrap suite).
	practiceAbsent bool
	// practiceReadErr makes every practice read fail for a reason that is NOT
	// not-found.
	practiceReadErr bool
	// truncateByIDs stamps the truncation verdict on the collision hydrate the
	// way a clamped server request does.
	truncateByIDs bool
	// mutateErr fails the create_batch the way the SERVER refuses one — the
	// summary-and-name validator being the refusal this landing's own bodies can
	// provoke. A refused batch writes nothing, so the fake applies nothing either.
	mutateErr error
	// rawReadErr fails every RAW-graph read. It is what reaches the ORIGIN read's
	// failure arm specifically: a nil GraphCaller fails the earlier target-graph
	// probe instead, so a cell that used one would assert a refusal it did not
	// mean and pass whatever the origin read did.
	rawReadErr bool

	reads     []landingRead
	mutations []*knowledgev1.MutationPlan
	execCalls int

	// duplicateWrites records every id a create plan wrote that the fake ALREADY
	// held. create_batch is an ADD: the server's applyCreate probes only edge
	// endpoints, and store.Graph.AddNode ends in g.nodes.Store(live.Id, live),
	// which replaces the resident row wholesale. So a repeated id is not refused
	// anywhere — it is a silent overwrite, and recording it here is what turns
	// that hazard into a red rather than a passing run.
	duplicateWrites []string
}

// applyPlan writes a create plan back into the fake the way a store would, so a
// SECOND and THIRD landing read what the run before it actually wrote.
//
// A FAKE THAT ONLY RECORDS PLANS CANNOT SEE THE DEFECT THIS EXISTS FOR: every
// run would read the same seeded population and mint the same twin id forever,
// which is exactly what a test seeding a resident by hand cannot distinguish from
// a counter that walks.
func (c *landingCaller) applyPlan(m *knowledgev1.MutationPlan) {
	for _, b := range m.GetNodeBodies() {
		id := b.GetId()
		if id == "" {
			continue
		}
		if _, dup := c.practiceByID[id]; dup {
			c.duplicateWrites = append(c.duplicateWrites, id)
		}
		n := &knowledgev1.Node{
			Id: id, Type: b.GetType(), SymbolName: b.GetName(),
			Description: b.GetDescription(), Summary: b.GetSummary(), Content: b.GetContent(),
			Status: b.GetStatus(), Metadata: maps.Clone(b.GetMetadata()), Source: b.GetSource(),
		}
		c.practiceByID[id] = n
		if b.GetType() == string(kgtypes.NodeSource) {
			c.practiceHubs = append(c.practiceHubs, n)
		}
	}
}

func (c *landingCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.execCalls++
	if m := req.GetMutation(); m != nil {
		c.mutations = append(c.mutations, m)
		if c.mutateErr != nil {
			return nil, c.mutateErr
		}
		c.applyPlan(m)
		return &knowledgev1.ExecuteResponse{AffectedCount: int64(len(m.GetNodeBodies()))}, nil
	}
	q := req.GetQuery()
	g := req.GetTarget().GetGraph()
	rec := landingRead{
		graph:    g,
		name:     req.GetTarget().GetName(),
		language: req.GetTarget().GetLanguage(),
		ids:      q.GetIds(),
		nodeType: q.GetSelection().GetNodeType(),
	}
	if preds := q.GetSelection().GetMetadataPredicates(); len(preds) > 0 {
		rec.meta = map[string]string{}
		for _, p := range preds {
			rec.meta[p.GetKey()] = p.GetValue()
		}
	}
	c.reads = append(c.reads, rec)

	if g == string(kgtypes.GraphPractice) {
		switch {
		case c.practiceAbsent:
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("graph practice/default not found"))
		case c.practiceReadErr:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("practice read exploded"))
		}
		if len(rec.ids) > 0 {
			out := &knowledgev1.ExecuteResponse{Truncated: c.truncateByIDs}
			for _, id := range rec.ids {
				if n := c.practiceByID[id]; n != nil {
					if n.GetTombstonedAt() != 0 && !q.GetIncludeTombstones() {
						continue
					}
					out.Nodes = append(out.Nodes, n)
				}
			}
			return out, nil
		}
		// The hub probe: a by-type browse narrowed on the hub key, drained through
		// the keyset walk below like any other browse.
		return &knowledgev1.ExecuteResponse{Nodes: keysetPage(c.practiceHubs, q)}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// The RAW graph read — both the source view load and the root-node probe the
	// origin read issues.
	if c.rawReadErr {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("raw graph read exploded"))
	}
	return &knowledgev1.ExecuteResponse{Nodes: keysetPage(c.sourceNodes, q)}, nil
}

// keysetPage models the server's id-keyset browse: rows in id-ascending order,
// starting after the cursor, bounded by the plan's limit.
//
// IT IS NOT A CONVENIENCE. foundation's browse drains by re-issuing with the last
// id as the cursor and stops when a page comes back short, so a fake that
// returned its whole set on every page would spin forever on any set at or above
// the page size — which is exactly what the beyond-500-rows input class drives,
// and exactly how this helper came to be written.
func keysetPage(all []*knowledgev1.Node, q *knowledgev1.QueryPlan) []*knowledgev1.Node {
	sorted := append([]*knowledgev1.Node(nil), all...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetId() < sorted[j].GetId() })

	after := q.GetAfterId()
	limit := int(q.GetLimit())
	var out []*knowledgev1.Node
	for _, n := range sorted {
		if after != "" && n.GetId() <= after {
			continue
		}
		out = append(out, n)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// landingSourceRows is the raw web graph a landing reads: one root `page` node
// carrying the recorded seed_host the origin read wants, and two sections.
func landingSourceRows() []*knowledgev1.Node {
	return []*knowledgev1.Node{
		{Id: "root", Type: "page", SymbolName: "EIP", Metadata: map[string]string{"seed_host": "www.enterpriseintegrationpatterns.com"}},
		{Id: "s1", Type: "section", SymbolName: "Message Router"},
		{Id: "s2", Type: "section", SymbolName: "Message Channel"},
	}
}

func newLandingCaller() *landingCaller {
	return &landingCaller{
		sourceNodes:  landingSourceRows(),
		practiceByID: map[string]*knowledgev1.Node{},
	}
}

const landingBody = `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
    summary := section.symbol_name
}`

// landParams builds a landing collect payload, with extra merged over it.
func landParams(t *testing.T, extra map[string]any) kgtools.CallToolParams {
	t.Helper()
	args := map[string]any{
		"type":        "web",
		"id":          "hohpe-eip",
		"transformer": "recipe",
		"recipe_body": landingBody,
		"land":        true,
	}
	maps.Copy(args, extra)
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "collect", Arguments: raw}
}

// runLand drives InterceptCollect over the fake and requires a successful run.
func runLand(t *testing.T, c *landingCaller, extra map[string]any) (string, *knowledgev1.MutationPlan) {
	t.Helper()
	before := len(c.mutations)
	deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
	handled, res := InterceptCollect(opCtx(), deps, landParams(t, extra))
	require.True(t, handled)
	require.False(t, res.IsError, "expected a successful landing, got: %s", resultText(res))
	require.Len(t, c.mutations, before+1, "a landing writes exactly ONE create_batch")
	return resultText(res), c.mutations[len(c.mutations)-1]
}

// practiceByIDReads counts the by-ids hydrates issued against the target graph in
// one slice of the fake's read log, so a caller can state a single run's read cost
// rather than the whole test's.
func practiceByIDReads(reads []landingRead) int {
	var n int
	for _, r := range reads {
		if r.graph == string(kgtypes.GraphPractice) && len(r.ids) > 0 {
			n++
		}
	}
	return n
}

// refuseLand drives InterceptCollect and requires a refusal, returning its text.
func refuseLand(t *testing.T, c *landingCaller, extra map[string]any) string {
	t.Helper()
	deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
	handled, res := InterceptCollect(opCtx(), deps, landParams(t, extra))
	require.True(t, handled, "a refusal is still handled client-side")
	require.True(t, res.IsError, "expected a refusal, got: %s", resultText(res))
	assert.Empty(t, c.mutations, "a refused landing writes NOTHING")
	return resultText(res)
}

// wantHubID is the deterministic hub id the landing mints for the eip slug.
func wantHubID() string {
	return recipe.StableID(landingTargetKey(), "hohpe-eip", string(kgtypes.NodeSource), "hohpe-eip")
}

// bodyByName finds a created body by its name.
func bodyByName(plan *knowledgev1.MutationPlan, name string) *knowledgev1.NodeBody {
	for _, b := range plan.GetNodeBodies() {
		if b.GetName() == name {
			return b
		}
	}
	return nil
}

// seedResidentHub puts a hub in the fake the way the graph holds one: findable by
// the preflight's TYPE+META browse AND resolvable by id.
//
// BOTH VIEWS, because both are read. The preflight browses for the hub to decide
// whether to create it; the write path resolves it by id to hold it to the hub
// contract. A fake that seeded only the browse reported a resident hub the write
// could not resolve, which is a state no graph can be in.
func seedResidentHub(c *landingCaller, id string) {
	hub := &knowledgev1.Node{
		Id: id, Type: string(kgtypes.NodeSource),
		Metadata: map[string]string{kgtypes.MetaKeySourceHub: id},
	}
	c.practiceHubs = append(c.practiceHubs, hub)
	c.practiceByID[id] = hub
}
