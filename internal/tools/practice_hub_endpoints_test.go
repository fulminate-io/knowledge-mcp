// SPDX-License-Identifier: Apache-2.0

package tools

// practice_hub_endpoints_test.go drives the `source_hub` selector on the two
// practice EDGE arms through the REAL mutate intercept: mutate(link), which the
// intra-practice arm claims client-side, and mutate(unlink), which declines to
// the engine. Every row goes through InterceptMutate rather than calling the
// guard directly, so a row measures the arm a caller actually reaches rather
// than a helper a caller cannot address.

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// The two opaque hub ids these rows carry. Opaque BY CONSTRUCTION: a hub is
// addressed by node id, and a guard that only worked for ids shaped like a
// language name would pass a test written with "go".
const (
	hubEndpointsHubA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hubEndpointsHubB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// practiceHubNodeResult seeds a practice node carrying a source_hub metadata
// key. A hub of "" seeds a node with an EMPTY metadata map, which is the
// grouped-under-nothing state — distinct from a node that is absent entirely.
func practiceHubNodeResult(t *testing.T, id, typ, hub string) kgtools.ToolResult {
	t.Helper()
	meta := map[string]string{}
	if hub != "" {
		meta["source_hub"] = hub
	}
	return nodeResultJSON(t, id, typ, meta)
}

// practiceHubFake seeds the combined practice graph with four endpoint shapes
// and NOTHING in knowledge, which is what makes the FROM-first probe confirm a
// foreign FROM and hand the call to the practice arms.
func practiceHubFake(t *testing.T) *fakeGraphCaller {
	t.Helper()
	return &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{"knowledge": {}},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: workingset.DefaultInstanceName}: {
				"pat-1":    practiceHubNodeResult(t, "pat-1", "pattern", hubEndpointsHubA),
				"uc-1":     practiceHubNodeResult(t, "uc-1", "use_case", hubEndpointsHubA),
				"uc-2":     practiceHubNodeResult(t, "uc-2", "use_case", hubEndpointsHubB),
				"orphan-1": practiceHubNodeResult(t, "orphan-1", "use_case", ""),
				// The hubs themselves — see practiceHubTargetFake's note: a hub id
				// that resolves to no node is refused on every arm now, so a fake
				// that seeds only members refuses every hub-scoped row.
				hubEndpointsHubA: nodeResultJSON(t, hubEndpointsHubA, string(kgtypes.NodeSource), nil),
				hubEndpointsHubB: nodeResultJSON(t, hubEndpointsHubB, string(kgtypes.NodeSource), nil),
			},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"practice", "default"}),
	}
}

// driveHubMutate runs one payload through the real mutate intercept against a
// freshly seeded fake and returns both, so a row can assert on the writes.
func driveHubMutate(t *testing.T, payload string) (*fakeGraphCaller, bool, kgtools.ToolResult) {
	t.Helper()
	fc := practiceHubFake(t)
	handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate", Arguments: json.RawMessage(payload),
	})
	assertHubDriveCannotSplit(t, fc, payload, res)
	return fc, handled, res
}

// hubEndpointReads returns every PLURAL-ids query the drive issued, which is the
// shape the hub resolve takes. Used by the read-count row.
func hubEndpointReads(fc *fakeGraphCaller) [][]string {
	var reads [][]string
	for _, req := range fc.execRequests {
		q, isQuery := req.GetPlan().(*knowledgev1.ExecuteRequest_Query)
		if !isQuery {
			continue
		}
		if ids := q.Query.GetIds(); len(ids) > 0 && q.Query.GetById() == "" {
			reads = append(reads, ids)
		}
	}
	return reads
}

// targetJSON renders an ExecuteRequest's GraphSelector as bytes, so two drives
// can be compared for a BYTE-IDENTICAL envelope rather than for a field a
// reader happened to check.
func targetJSON(t *testing.T, req *knowledgev1.ExecuteRequest) string {
	t.Helper()
	b, err := json.Marshal(req.GetTarget())
	require.NoError(t, err)
	return string(b)
}

// lastMutationRequest returns the last ExecuteRequest carrying a Mutation.
func lastMutationRequest(t *testing.T, fc *fakeGraphCaller) *knowledgev1.ExecuteRequest {
	t.Helper()
	for _, v := range slices.Backward(fc.execRequests) {
		if _, isMutation := v.GetPlan().(*knowledgev1.ExecuteRequest_Mutation); isMutation {
			return v
		}
	}
	t.Fatalf("no mutation ExecuteRequest was issued")
	return nil
}

// TestPracticeLinkHub_ScopesTheEndpoints is requirement 3's link arm: a
// mutate(link, graph:"practice") carrying source_hub is served when BOTH
// endpoints are grouped under that hub, and refused naming the hub, the endpoint
// and its actual hub or absence otherwise.
func TestPracticeLinkHub_ScopesTheEndpoints(t *testing.T) {
	t.Run("both endpoints under the hub: the edge is created", func(t *testing.T) {
		fc, handled, res := driveHubMutate(t,
			`{"operation":"link","graph":"practice","from":"pat-1","to":"uc-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled, "a hub-scoped practice link is claimed by the intra-practice arm")
		require.False(t, res.IsError, "both endpoints are under the named hub: %s", toolResultText(res))

		require.Len(t, fc.execMutations, 1, "exactly one LINK is issued")
		plan := fc.execMutations[0]
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, plan.GetKind())
		assert.Equal(t, []string{"pat-1"}, plan.GetSelection().GetIds())
		assert.Equal(t, "uc-1", plan.GetEdgeSpec().GetToId())

		// NOTHING HUB-RELATED ON THE WIRE. The hub scoped a client-side
		// resolution; it is not a field the plan carries, and the envelope is
		// the same practice selector a hub-less link compiles to.
		planJSON, err := json.Marshal(plan)
		require.NoError(t, err)
		assert.NotContains(t, string(planJSON), hubEndpointsHubA,
			"the hub scoped the resolve client-side and must not ride the MutationPlan")
		assert.NotContains(t, string(planJSON), "source_hub")

		bare, bareHandled, bareRes := driveHubMutate(t,
			`{"operation":"link","graph":"practice","from":"pat-1","to":"uc-1","relationship":"contains"}`)
		require.True(t, bareHandled)
		require.False(t, bareRes.IsError, toolResultText(bareRes))
		assert.Equal(t, targetJSON(t, lastMutationRequest(t, bare)), targetJSON(t, lastMutationRequest(t, fc)),
			"the GraphSelector is byte-identical with and without the hub — no wire field moved")
	})

	t.Run("to under another hub is refused naming both hubs", func(t *testing.T) {
		fc, handled, res := driveHubMutate(t,
			`{"operation":"link","graph":"practice","from":"pat-1","to":"uc-2","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled, "a refusal is CLAIMED, never passed on")
		require.True(t, res.IsError, "an endpoint outside the hub is bad input, not a no-op")
		body := toolResultText(res)
		assert.Contains(t, body, hubEndpointsHubA, "the refusal names the hub asked for")
		assert.Contains(t, body, "`to`", "the refusal names which endpoint failed")
		assert.Contains(t, body, "uc-2", "the refusal names the endpoint id")
		assert.Contains(t, body, hubEndpointsHubB, "the refusal names the endpoint's ACTUAL hub")
		assert.Empty(t, fc.execMutations, "no mutation is issued before the refusal")
	})

	t.Run("from carrying no hub key is refused naming the absence", func(t *testing.T) {
		fc, handled, res := driveHubMutate(t,
			`{"operation":"link","graph":"practice","from":"orphan-1","to":"uc-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled)
		require.True(t, res.IsError, "an endpoint under no hub is bad input, not a no-op")
		body := toolResultText(res)
		assert.Contains(t, body, hubEndpointsHubA)
		assert.Contains(t, body, "`from`")
		assert.Contains(t, body, "orphan-1")
		assert.Contains(t, body, "no source_hub metadata key",
			"the absence is named as an absence, not reported as a different hub")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("an endpoint absent from the graph is refused naming the absence", func(t *testing.T) {
		fc, handled, res := driveHubMutate(t,
			`{"operation":"link","graph":"practice","from":"pat-1","to":"missing-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled,
			"a hub-scoped link does NOT fall through when an endpoint is missing: falling through would reach "+
				"the generic composer, which ignores the hub")
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "missing-1")
		assert.Contains(t, body, "resolves to no node")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("the endpoint hubs resolve in ONE read over both ids", func(t *testing.T) {
		fc, _, res := driveHubMutate(t,
			`{"operation":"link","graph":"practice","from":"pat-1","to":"uc-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.False(t, res.IsError, toolResultText(res))
		reads := hubEndpointReads(fc)
		require.Len(t, reads, 1, "the hub resolve is ONE bulk read, not one read per endpoint")
		// THE ONE READ CARRIES THE HUB AND BOTH ENDPOINTS. The hub resolution
		// folds its own id into the read the endpoint scope already paid, so the
		// call still costs exactly one round trip and the list names all three.
		assert.Equal(t, []string{"pat-1", "uc-1"}, reads[0],
			"and it carries both endpoint ids — the HUB's own validity is resolved one layer down, in the "+
				"engine, so this read is the endpoint scope's alone")
	})

	t.Run("no source_hub reads the whole graph, across hubs", func(t *testing.T) {
		// THE KNOWN POSITIVE for every refusal above. pat-1 and uc-2 sit under
		// DIFFERENT hubs, and a link naming no hub joins them, which is the
		// omitted-means-the-whole-graph reading stated as a behaviour rather
		// than as an absence of refusals.
		fc, handled, res := driveHubMutate(t,
			`{"operation":"link","graph":"practice","from":"pat-1","to":"uc-2","relationship":"contains"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "a hub-less link is unchanged: %s", toolResultText(res))
		require.Len(t, fc.execMutations, 1)
		assert.Empty(t, hubEndpointReads(fc), "and it pays no endpoint-hub read at all")
	})

	t.Run("a hub-scoped link never falls through when its own probe fails", func(t *testing.T) {
		// THE ONE PATH FROM THE GUARD TO THE FALL-THROUGH. The guard's bulk read
		// resolves both endpoints under the hub, and the arm's own by-id probe
		// then fails — which before the conjunct would return (false,_), hand
		// the call to the generic composer, and drop the hub silently. Driven by
		// failing ONLY the single-id read.
		fc := practiceHubFake(t)
		fc.queryErrors = map[string]error{"pat-1": errors.New("probe read failed")}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate",
			Arguments: json.RawMessage(`{"operation":"link","graph":"practice","from":"pat-1","to":"uc-1",` +
				`"relationship":"contains","source_hub":"` + hubEndpointsHubA + `"}`),
		})
		require.True(t, handled, "a hub-scoped link is never passed on to a composer that ignores the hub")
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "could not read")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("language is still refused, naming the replacement", func(t *testing.T) {
		_, handled, res := driveHubMutate(t,
			`{"operation":"link","graph":"practice","language":"go","from":"pat-1","to":"uc-1","relationship":"contains"}`)
		require.True(t, handled)
		require.True(t, res.IsError, "`language` on a practice link stays refused")
		assert.Contains(t, toolResultText(res), "source_hub")
	})

	t.Run("source_hub off the practice family is refused", func(t *testing.T) {
		_, handled, res := driveHubMutate(t,
			// NO `name` on this row: the cross-graph composer's own surface
			// rejects it, and that refusal would mask the one under test.
			`{"operation":"link","graph":"web","from":"pat-1","to":"uc-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled)
		require.True(t, res.IsError, "a hub on a non-practice link reaches nothing and is refused")
		assert.Contains(t, toolResultText(res), "source_hub")
	})
}

// TestPracticeUnlinkHub_ScopesTheEndpoints is requirement 3's unlink arm. The
// unlink DECLINES to the engine, so an accepted call is observed as
// handled==false with zero client writes and a compiled plan that carries the
// two endpoint ids and nothing hub-related.
func TestPracticeUnlinkHub_ScopesTheEndpoints(t *testing.T) {
	t.Run("both endpoints under the hub: the unlink is passed on", func(t *testing.T) {
		const payload = `{"operation":"unlink","graph":"practice","from":"pat-1","to":"uc-1",` +
			`"relationship":"contains","source_hub":"` + hubEndpointsHubA + `"}`
		fc, handled, res := driveHubMutate(t, payload)
		assert.False(t, handled, "an accepted practice unlink declines to the engine, which owns the write")
		assert.False(t, res.IsError, toolResultText(res))
		assert.Empty(t, fc.execMutations, "the client issues no write of its own")

		req, ok := engine.Compile("mutate", json.RawMessage(payload))
		require.True(t, ok, "the declined unlink must compile")
		m, isMutation := req.GetPlan().(*knowledgev1.ExecuteRequest_Mutation)
		require.True(t, isMutation)
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UNLINK, m.Mutation.GetKind())
		assert.Equal(t, []string{"pat-1"}, m.Mutation.GetSelection().GetIds())
		assert.Equal(t, "uc-1", m.Mutation.GetEdgeSpec().GetToId())
		planJSON, err := json.Marshal(m.Mutation)
		require.NoError(t, err)
		assert.NotContains(t, string(planJSON), hubEndpointsHubA, "nothing hub-related rides the wire")

		bare, bok := engine.Compile("mutate", json.RawMessage(
			`{"operation":"unlink","graph":"practice","from":"pat-1","to":"uc-1","relationship":"contains"}`))
		require.True(t, bok)
		assert.Equal(t, targetJSON(t, bare), targetJSON(t, req),
			"the GraphSelector is byte-identical with and without the hub")
	})

	t.Run("to under another hub is refused naming both hubs", func(t *testing.T) {
		fc, handled, res := driveHubMutate(t,
			`{"operation":"unlink","graph":"practice","from":"pat-1","to":"uc-2","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled, "a refusal is CLAIMED rather than declined to the engine")
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, hubEndpointsHubA)
		assert.Contains(t, body, "`to`")
		assert.Contains(t, body, "uc-2")
		assert.Contains(t, body, hubEndpointsHubB)
		assert.Empty(t, fc.execMutations)
	})

	t.Run("from carrying no hub key is refused naming the absence", func(t *testing.T) {
		fc, handled, res := driveHubMutate(t,
			`{"operation":"unlink","graph":"practice","from":"orphan-1","to":"uc-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled)
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "`from`")
		assert.Contains(t, body, "orphan-1")
		assert.Contains(t, body, "no source_hub metadata key")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("an endpoint absent from the graph is refused naming the absence", func(t *testing.T) {
		fc, handled, res := driveHubMutate(t,
			`{"operation":"unlink","graph":"practice","from":"pat-1","to":"missing-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled)
		require.True(t, res.IsError)
		assert.Contains(t, toolResultText(res), "missing-1")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("the endpoint hubs resolve in ONE read over both ids", func(t *testing.T) {
		fc, _, res := driveHubMutate(t,
			`{"operation":"unlink","graph":"practice","from":"pat-1","to":"uc-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.False(t, res.IsError, toolResultText(res))
		reads := hubEndpointReads(fc)
		require.Len(t, reads, 1, "the hub resolve is ONE bulk read, not one read per endpoint")
		assert.Equal(t, []string{"pat-1", "uc-1"}, reads[0])
	})

	t.Run("no source_hub reads the whole graph, across hubs", func(t *testing.T) {
		fc, handled, res := driveHubMutate(t,
			`{"operation":"unlink","graph":"practice","from":"pat-1","to":"uc-2","relationship":"contains"}`)
		assert.False(t, handled, "a hub-less practice unlink declines exactly as it always has")
		assert.False(t, res.IsError, toolResultText(res))
		assert.Empty(t, hubEndpointReads(fc), "and it pays no endpoint-hub read at all")
	})

	t.Run("language is still refused, naming the replacement", func(t *testing.T) {
		_, handled, res := driveHubMutate(t,
			`{"operation":"unlink","graph":"practice","language":"go","from":"pat-1","to":"uc-1","relationship":"contains"}`)
		require.True(t, handled)
		require.True(t, res.IsError, "`language` on a practice unlink stays refused")
		assert.Contains(t, toolResultText(res), "source_hub")
	})

	t.Run("source_hub off the practice family is refused", func(t *testing.T) {
		_, handled, res := driveHubMutate(t,
			`{"operation":"unlink","graph":"web","name":"doc","from":"pat-1","to":"uc-1","relationship":"contains",`+
				`"source_hub":"`+hubEndpointsHubA+`"}`)
		require.True(t, handled)
		require.True(t, res.IsError, "a hub on a non-practice unlink reaches nothing and is refused")
		assert.Contains(t, toolResultText(res), "source_hub")
	})
}

// TestPracticeEndpointHub_TombstonedEndpointIsStillAMember is the ENDPOINT
// guard's own observer for the tombstone-visibility choice, and it exists
// because the sibling targets file's row does not reach this call site: flipping
// foundation.IncludeTombstones at practice_hub_endpoints.go:75 to
// ExcludeTombstones left the whole tools and engine suites green, so the choice
// was made in one file and observed only in the other.
//
// THE CHOICE IT PINS, stated as the decision rather than as the code: a
// soft-deleted practice node is STILL a member of the hub it was grouped under.
// The question the guard asks is membership, not liveness, and the arm's own
// by-id probe reads tombstones too — answering on a different visibility rule
// would let the two disagree about whether an endpoint exists at all, and would
// make a by-hub soft delete newly refuse writes against the nodes it hid.
//
// IT DRIVES THE TARGET FAKE, deliberately. practiceHubFake above models no
// tombstone at all, and a fake that cannot hide a row cannot observe a guard that
// asks it to. practiceHubTargetFake already seeds hubTargetTomb under
// hubEndpointsHubA and carries it in tombstonedIDs, so the fixture the endpoint
// rows need is the one that exists rather than a second copy of it.
func TestPracticeEndpointHub_TombstonedEndpointIsStillAMember(t *testing.T) {
	for _, op := range []string{"link", "unlink"} {
		t.Run(op+"/a tombstoned `from` is served, not refused as absent", func(t *testing.T) {
			fc, _, res := driveHubTarget(t,
				`{"operation":"`+op+`","graph":"practice","from":"`+hubTargetTomb+`","to":"`+hubTargetMemberB+
					`","relationship":"contains","source_hub":"`+hubEndpointsHubA+`"}`)
			assert.False(t, res.IsError,
				"the endpoint guard reads WITH tombstones, so a soft-deleted endpoint is still under its "+
					"hub: %s", toolResultText(res))
			// A practice LINK is claimed by the intra-practice arm and never reaches
			// the engine, so it pays the endpoint scope's read alone; an UNLINK
			// declines and pays the engine's hub read as well.
			wantReads := 1
			if op == "unlink" {
				wantReads = 2
			}
			require.Len(t, hubEndpointReads(fc), wantReads, "the endpoint scope resolved the pair")
			assert.Equal(t, []string{hubTargetTomb, hubTargetMemberB},
				hubEndpointReads(fc)[0], "the endpoint scope's read carries both endpoints")
		})

		t.Run(op+"/a tombstoned `to` is served too", func(t *testing.T) {
			_, _, res := driveHubTarget(t,
				`{"operation":"`+op+`","graph":"practice","from":"`+hubTargetMemberB+`","to":"`+hubTargetTomb+
					`","relationship":"contains","source_hub":"`+hubEndpointsHubA+`"}`)
			assert.False(t, res.IsError,
				"the rule is the endpoint's membership, not which role it plays: %s", toolResultText(res))
		})
	}

	t.Run("the control: the harness really does hide a tombstone from a tombstone-blind read", func(t *testing.T) {
		// THE SAME-RUN KNOWN POSITIVE for the four rows above. Without it, "the
		// guard served the tombstoned endpoint" is equally true of a harness that
		// never modeled tombstones — which is exactly the state that left this
		// call site unobserved.
		fc := practiceHubTargetFake(t)
		blind, _, err := fetchPracticeNodesForTest(fc, []string{hubTargetTomb}, false)
		require.NoError(t, err)
		assert.NotContains(t, blind, hubTargetTomb, "a tombstone-blind read must MISS the tombstoned id")
		seeing, _, err := fetchPracticeNodesForTest(fc, []string{hubTargetTomb}, true)
		require.NoError(t, err)
		assert.Contains(t, seeing, hubTargetTomb, "and a tombstone-including read must serve it")
	})
}

// TestPracticeEndpointHub_ReadFailuresRefuse drives the two ways the ENDPOINT
// guard can fail to SEE its endpoints. Each is a refusal rather than a pass,
// because a guard that could not read its endpoints has not checked them.
//
// THE TRUNCATED ROW EXISTS BECAUSE THE ARM WAS UNOBSERVED. Holing the truncation
// refusal at practice_hub_endpoints.go left the whole tools and engine suites
// green: the sibling targets file drives its own truncated read and this one had
// none. The arm's own comment says two ids cannot reach the server's row ceiling,
// which is why it is not a LIVE failure mode — but FetchNodesByIDs reports
// truncation rather than acting on it, so the decision to refuse is this caller's
// and is testable on the same knob the targets rows use. An arm whose absence
// changes nothing is not a guard.
func TestPracticeEndpointHub_ReadFailuresRefuse(t *testing.T) {
	for _, op := range []string{"link", "unlink"} {
		payload := `{"operation":"` + op + `","graph":"practice","from":"pat-1","to":"uc-1",` +
			`"relationship":"contains","source_hub":"` + hubEndpointsHubA + `"}`

		t.Run(op+"/a TRUNCATED read refuses rather than treating an unseen endpoint as absent", func(t *testing.T) {
			fc := practiceHubFake(t)
			fc.bulkTruncated = true
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(payload),
			})
			require.True(t, handled, "an unreadable scope is never passed on to a path that ignores the hub")
			require.True(t, res.IsError)
			assert.Contains(t, toolResultText(res), "TRUNCATED")
			assert.Empty(t, fc.execMutations, "nothing is written before the refusal")
		})

		t.Run(op+"/a failed bulk read refuses, naming that nothing was written", func(t *testing.T) {
			fc := practiceHubFake(t)
			fc.bulkQueryErr = errors.New("endpoint read failed")
			handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name: "mutate", Arguments: json.RawMessage(payload),
			})
			require.True(t, handled)
			require.True(t, res.IsError)
			assert.Contains(t, toolResultText(res), "could not be read")
			assert.Empty(t, fc.execMutations)
		})

		t.Run(op+"/the control: the same call serves when the read succeeds whole", func(t *testing.T) {
			// The same-run KNOWN POSITIVE for the two refusals above: without it a
			// row could be red for any reason and still read as the arm firing.
			_, _, res := driveHubMutate(t, payload)
			assert.Falsef(t, res.IsError, "%s must serve on a whole read: %s", op, toolResultText(res))
		})
	}
}
