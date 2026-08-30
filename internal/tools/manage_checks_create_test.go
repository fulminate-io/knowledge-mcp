// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_create_test.go pins the four properties that make one-call
// authoring safe: nothing is written until the admission gate passes, the gate's
// own semantics survive the wrapper, a failed check write names the fixtures it
// orphaned, and a successful one leaves both display edges pointing the right way.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// checksWriteFake records every MutationPlan and answers each create_batch with
// one generated id PER NODE BODY.
//
// PER-CALL IDS ARE THE POINT. The package's shared fake answers every mutation
// from one seeded id slice, which cannot distinguish the two-fixture batch from
// the one-check batch — and this operation's whole sequencing depends on those
// being two different writes with two different id sets.
type checksWriteFake struct {
	plans []*knowledgev1.MutationPlan
	// targets records the graph each mutation was addressed to, in the same
	// order. Without it every assertion below would hold just as well for a
	// create that wrote all three nodes into the knowledge graph — the fake
	// answers whatever it is asked, so the selector has to be asserted rather
	// than assumed.
	targets []string
	// failOnNth fails the Nth mutation (1-based) and leaves the others
	// succeeding, which is how the orphaned-fixture branch is DRIVEN rather than
	// merely being present in the source.
	failOnNth int
	nextID    int
}

func (f *checksWriteFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	m := req.GetMutation()
	if m == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	f.plans = append(f.plans, m)
	f.targets = append(f.targets, req.GetTarget().GetGraph())
	if f.failOnNth == len(f.plans) {
		return nil, errors.New("store unavailable")
	}
	ids := make([]string, 0, len(m.GetNodeBodies()))
	for range m.GetNodeBodies() {
		f.nextID++
		ids = append(ids, fmt.Sprintf("written-node-%d", f.nextID))
	}
	return &knowledgev1.ExecuteResponse{Ids: ids, AffectedCount: int64(len(ids))}, nil
}

// createChecksArgs is a payload the admission gate ACCEPTS: the pattern fires on
// the bad fixture, is silent on the good one, and the good one fires again with
// the where-tree dropped, so the calibration probe passes too.
func createChecksArgs() map[string]any {
	return map[string]any{
		"operation":   OpChecksCreate,
		"name":        "bucket-count-from-a-bare-identifier-argument",
		"summary":     "a partition count computed from a bare identifier argument carries no provenance at the site",
		"description": "the rule, in prose",
		"language":    "go",
		"severity":    "warning",
		"check_type":  string(corpus.CheckAstPattern),
		"dsl_pattern": "searchengine.BucketCountFor(len($X))",
		"check_where": `{"kind": {"of": "X", "is": "identifier"}}`,
		"fixture_bad": map[string]any{
			"name":        "the-bad-example",
			"summary":     "BAD fixture: the count is derived from a bare parameter identifier",
			"description": "why this is the shape to avoid",
			"content":     "package fixture\n\nfunc bucketsForWindow(items []string) int {\n\treturn searchengine.BucketCountFor(len(items))\n}\n",
		},
		"fixture_good": map[string]any{
			"name":        "the-good-example",
			"summary":     "GOOD near-miss: the count is derived from a selector expression, so the where-tree narrows it out",
			"description": "why this conforms",
			"content":     "package fixture\n\ntype corpusView struct{ docs []string }\n\nfunc bucketsForCorpus(v corpusView) int {\n\treturn searchengine.BucketCountFor(len(v.docs))\n}\n",
		},
	}
}

// runChecksCreate drives the create operation over the write fake.
func runChecksCreate(t *testing.T, fake *checksWriteFake, args map[string]any) kgtools.ToolResult {
	t.Helper()
	body, err := json.Marshal(args)
	require.NoError(t, err)
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: fake}
	handled, res := InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage_checks", Arguments: body})
	require.True(t, handled)
	return res
}

// TestManageChecks_CreateRefusesSummarylessCheck is the characterization guard
// for the summary rule.
//
// IT IS NOT A NEW RULE AND MUST NOT BECOME ONE. The engine already refuses a
// summaryless node of a non-auto-summarized type; this asserts the tool routes
// THROUGH that rule rather than around it, and it asserts the ONE property a
// per-node server refusal cannot give a three-write sequence: the refusal lands
// before the FIRST write, so a summaryless check cannot leave two fixtures behind.
func TestManageChecks_CreateRefusesSummarylessCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		strip func(map[string]any)
	}{
		{"the check itself", func(a map[string]any) { a["summary"] = "" }},
		{"the bad fixture", func(a map[string]any) {
			a["fixture_bad"].(map[string]any)["summary"] = ""
		}},
		{"the good fixture", func(a map[string]any) {
			a["fixture_good"].(map[string]any)["summary"] = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &checksWriteFake{}
			args := createChecksArgs()
			tc.strip(args)

			res := runChecksCreate(t, fake, args)
			require.True(t, res.IsError, "a summaryless node must be refused")
			assert.Contains(t, res.Content[0].Text, "summary is required")
			assert.Empty(t, fake.plans,
				"the refusal must land BEFORE the first write, or a summaryless check leaves fixtures behind")
		})
	}

	// KNOWN POSITIVE: the same payload WITH every summary present is accepted and
	// does write. Without it, "zero mutations" above is equally satisfied by a
	// create that never writes anything at all.
	fake := &checksWriteFake{}
	res := runChecksCreate(t, fake, createChecksArgs())
	require.False(t, res.IsError, "the control payload must succeed: %s", res.Content[0].Text)
	require.Len(t, fake.plans, 2, "a successful create writes the fixtures and then the check")
}

// TestManageChecks_CreateRefusesSwappedFixturesAndWritesNothing is the
// proof-the-gate-can-fail control, made permanent.
//
// SWAPPING THE FIXTURES makes the check SILENT on its bad example — the exact
// state the admission gate exists to catch. The assertion is on the gate's own
// message, per-fixture match counts included, because those counts are what tells
// an author whether the check matched neither example or both.
func TestManageChecks_CreateRefusesSwappedFixturesAndWritesNothing(t *testing.T) {
	fake := &checksWriteFake{}
	args := createChecksArgs()
	args["fixture_bad"], args["fixture_good"] = args["fixture_good"], args["fixture_bad"]

	res := runChecksCreate(t, fake, args)
	require.True(t, res.IsError, "a check silent on its bad example must be refused")
	body := res.Content[0].Text
	assert.Contains(t, body, "SILENT on its bad example",
		"the gate's own diagnosis must survive the wrapper")
	assert.Contains(t, body, "bad matched 0",
		"the per-fixture match counts must be relayed — they are what makes the refusal actionable")
	assert.Contains(t, body, "nothing was written")
	assert.Empty(t, fake.plans, "a refused create must issue ZERO mutations")
}

// TestManageChecks_CreateNamesOrphanedFixturesWhenTheCheckWriteFails drives the
// one crash window this operation has.
//
// THE FAILURE IS INJECTED so the branch is EXERCISED rather than merely present:
// the fixture batch succeeds, the check batch errors, and the two example nodes
// are then real and bound by nothing. The correct behavior is to say so loudly —
// there is deliberately no rollback and no best-effort cleanup, because a silent
// compensator here is a fallback.
func TestManageChecks_CreateNamesOrphanedFixturesWhenTheCheckWriteFails(t *testing.T) {
	fake := &checksWriteFake{failOnNth: 2}

	res := runChecksCreate(t, fake, createChecksArgs())
	require.True(t, res.IsError, "a failed check write must not read as success")
	body := res.Content[0].Text

	require.Len(t, fake.plans, 2, "the fixtures must have been written before the check write failed")
	written := fake.plans[0]
	require.Len(t, written.GetNodeBodies(), 2)

	// BOTH ids, asserted individually rather than as a count: an error naming one
	// of them leaves the other silently orphaned.
	assert.Contains(t, body, "written-node-1", "the error must name the first orphaned fixture")
	assert.Contains(t, body, "written-node-2", "the error must name the second orphaned fixture")
	assert.Contains(t, body, "ORPHANED", "the reader must be told what state the graph is in")
	assert.Contains(t, body, OpChecksList, "the error must point at the surface that lists unbound fixtures")

	// NO SILENT CLEANUP: exactly two mutations were issued, so nothing tried to
	// delete the fixtures behind the caller's back.
	assert.Len(t, fake.plans, 2, "a compensating delete would show up as a third mutation")
}

// TestManageChecks_CreateBornLinksBothFixtures asserts BOTH display edges land,
// each in its own direction with its own relationship.
//
// ASSERTED PER NAMED EDGE, NEVER AS A COUNT OF TWO: an implementation that drew
// both edges to the same fixture, or both with the same relationship, satisfies a
// count and produces a graph in which half the bindings are wrong.
func TestManageChecks_CreateBornLinksBothFixtures(t *testing.T) {
	fake := &checksWriteFake{}
	res := runChecksCreate(t, fake, createChecksArgs())
	require.False(t, res.IsError, "the create must succeed: %s", res.Content[0].Text)
	require.Len(t, fake.plans, 2)

	// EVERY WRITE IS ADDRESSED TO THE CHECKS GRAPH. Checks and their fixtures
	// live together and the binding never crosses a graph boundary, so a create
	// that wrote them into knowledge would produce a corpus no scan can read.
	assert.Equal(t, []string{string(kgtypes.GraphChecks), string(kgtypes.GraphChecks)}, fake.targets,
		"both writes must address the checks graph")

	fixtures := fake.plans[0]
	require.Len(t, fixtures.GetNodeBodies(), 2, "both fixtures ride ONE atomic batch")
	badID, goodID := "written-node-1", "written-node-2"

	check := fake.plans[1]
	require.Len(t, check.GetNodeBodies(), 1)
	md := check.GetNodeBodies()[0].GetMetadata()
	assert.Equal(t, badID, md[corpus.MetaFixtureBad], "the metadata binding is what every executor reads")
	assert.Equal(t, goodID, md[corpus.MetaFixtureGood])

	// The edges, one assertion per (target, relationship) pair.
	edges := map[string]string{}
	for _, e := range check.GetEdges() {
		require.EqualValues(t, 0, e.GetFromIdx(), "both edges leave the check node")
		edges[e.GetToId()] = e.GetType()
	}
	assert.Equal(t, string(kgtypes.EdgeAvoidWhen), edges[badID],
		"check --avoid-when--> the bad fixture: the shape the check fires on is the one to avoid")
	assert.Equal(t, string(kgtypes.EdgeAppliesWhen), edges[goodID],
		"check --applies-when--> the good fixture: the conforming near-miss")
	assert.NotEqual(t, edges[badID], edges[goodID],
		"two edges carrying the same relationship would leave the direction unrecoverable")
}
