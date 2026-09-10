// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// account_accepted_and_ignored_test.go pins the CLIENT half of the
// accepted-and-ignored contract for the `account` tool parameter.
//
// THE RULING THIS ENFORCES, verbatim: "Keep the `account` parameter on the five
// tool surfaces that still advertise it, accepted and ignored, with the pinned
// wording stating it does nothing; no schema change in this project and no
// follow-on ticket."
//
// The parameter is advertised by five tools and consumed by no graph family: the
// families that keyed on it are retired, and a collected inventory graph is a
// registered custom type addressed by name. A surface that advertises a
// parameter, documents that it does nothing, and then errors when a caller sends
// it is telling the caller two different things — which is the defect these rows
// close.
//
// EVERY BEHAVIORAL ROW IS A SAME-RUN PAIR: the same call twice, once without the
// field and once with it, compared verbatim. A one-sided "it does not error"
// assertion would pass against a gate that had stopped checking anything, so
// every table carries a known-positive control naming a param the gate must
// still refuse.

// accountProbeValue is the value every pair below sends. It names no instance of
// any surviving family, so an arm that started ROUTING it rather than ignoring it
// would fail on the lookup rather than pass quietly.
const accountProbeValue = "ignored-value"

// payloadWithAccount and payloadWithout are the two halves of every gate pair.
// They differ in exactly one key.
var (
	payloadWithout    = json.RawMessage(`{}`)
	payloadWithAccnt  = json.RawMessage(`{"account":"` + accountProbeValue + `"}`)
	payloadWithForeig = json.RawMessage(`{"resource_type":"ec2"}`)
)

// gateAnswer renders a gate outcome as a comparable string: the empty string for
// acceptance, the verbatim message otherwise. Comparing the RENDERED outcome is
// what makes the pair a byte compare rather than a shared-sentinel check.
func gateAnswer(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestQueryArmRegistry_AccountIsDeliberatelyIgnoredOnEveryArm is requirement 1's
// declaration half. The query surface's refusal is a REGISTRY CELL — the
// per-arm classification the gate reads — so the cell is what has to change, and
// a behavioral test alone would leave the table free to drift back.
//
// EVERY ARM, not a sample. The registry is assembled from four sibling files and
// a new arm inherits nothing, so a rule stated over a sample is a rule a new arm
// escapes on the day it lands.
//
// TWO CLASSES SATISFY THE RULING AND ONE VIOLATES IT. An arm that CONSUMES the
// param routes it onto the Target and sends it to the server, whose selector gate
// ignores it — no refusal, and the class is an accurate statement of what the
// client does. An arm that DELIBERATELY IGNORES it drops it with a justification.
// REJECTED is the class the ruling forbids, because that is the one that answers
// a caller with an error naming a parameter the same schema documents as inert.
func TestQueryArmRegistry_AccountIsDeliberatelyIgnoredOnEveryArm(t *testing.T) {
	require.NotEmpty(t, queryArmRegistry, "the registry must be assembled — an empty table proves nothing")

	ignoring, consuming := 0, 0
	for _, arm := range sortedArmIDs() {
		spec := queryArmRegistry[arm]
		t.Run(string(arm), func(t *testing.T) {
			class, ok := queryParamClass(arm, "account")
			require.Truef(t, ok, "arm %s must classify `account` — an unclassified param fails the partition", arm)
			assert.Falsef(t, spec.rejected["account"],
				"arm %s must NOT reject `account`: the owner's ruling is that the parameter is accepted "+
					"and ignored on every surface that advertises it, and this schema says so in its own wording", arm)

			switch class {
			case classDeliberatelyIgnored:
				ignoring++
				assert.NotEmptyf(t, spec.deliberatelyIgnored["account"],
					"arm %s must justify ignoring `account`, as every ignored cell is justified", arm)
			case classConsumed:
				consuming++
			case classRejected:
				t.Fatalf("arm %s rejects `account`, which the ruling forbids", arm)
			}
		})
	}

	// BOTH POPULATIONS MUST BE NON-EMPTY, or the loop above is measuring one
	// thing and reporting two. A zero ignoring count would mean the applier never
	// ran; a zero consuming count would mean the arms that genuinely route the
	// field had been relabeled into a claim the client does not honor.
	assert.NotZero(t, ignoring, "no arm ignores `account` — the ruling's applier did not run")
	assert.NotZero(t, consuming,
		"no arm consumes `account` — the composite-mode arms put it on the Target and must still say so")
}

// TestAccountQueryParams_AccountDoesNotChangeTheGateAnswer is requirement 1's
// behavioral half, driven through the gate every query claim point calls. The
// registry cell above says what the table declares; this says what a caller gets.
func TestAccountQueryParams_AccountDoesNotChangeTheGateAnswer(t *testing.T) {
	for _, arm := range sortedArmIDs() {
		t.Run(string(arm), func(t *testing.T) {
			without := gateAnswer(accountQueryParams(arm, payloadWithout))
			require.Emptyf(t, without,
				"the empty payload must pass arm %s's gate, or this pair cannot tell an ignored "+
					"account from a refusal the call was already getting", arm)

			with := gateAnswer(accountQueryParams(arm, payloadWithAccnt))
			assert.Equalf(t, without, with,
				"arm %s must answer identically with the ignored `account` field set, and it answered %q", arm, with)
		})
	}

	// KNOWN-POSITIVE CONTROL. Without it the table above is satisfied by a gate
	// that accepts everything — the failure mode of a "fix" that stopped
	// classifying. `resource_type` is the retired inventory browse's prefix
	// filter, rejected on every surviving arm, so its refusal proves the gate is
	// still running on the very payloads the rows above pass.
	t.Run("the gate still refuses an inapplicable param", func(t *testing.T) {
		refused := 0
		for _, arm := range sortedArmIDs() {
			if err := accountQueryParams(arm, payloadWithForeig); err != nil {
				refused++
				assert.Containsf(t, err.Error(), "resource_type",
					"arm %s's refusal must name the offending field", arm)
			}
		}
		require.NotZero(t, refused,
			"no arm refused `resource_type` — the gate is inert and every acceptance above is vacuous")
	})
}

// TestAccountParam_WordingIsByteIdenticalAcrossTheFiveSurfaces is requirement 4.
//
// AN EXISTING ROW ASSERTS EACH SURFACE CONTAINS THE SENTENCE
// (TestMutateSurface_DeclaresInstanceSelectors). Containment is not identity: a
// surface could carry the sentence plus an extra clause contradicting it, or two
// surfaces could drift into different phrasings around the same substring, and
// every containment row would stay green. The ruling pins the WORDING, so the
// wording is compared verbatim, surface against surface.
func TestAccountParam_WordingIsByteIdenticalAcrossTheFiveSurfaces(t *testing.T) {
	surfaces := []struct {
		tool  string
		props map[string]kgtools.Property
	}{
		{"mutate", mutateProperties()},
		{"delete", DeleteToolDef().InputSchema.Properties},
		{"query", QueryToolDef().InputSchema.Properties},
		{"search", SearchToolDef().InputSchema.Properties},
		{"traverse", TraverseToolDef().InputSchema.Properties},
	}

	first := ""
	firstTool := ""
	for _, s := range surfaces {
		p, ok := s.props["account"]
		require.Truef(t, ok, "%s must still declare `account` — the wire field survives the families that used it", s.tool)
		require.NotEmptyf(t, p.Description, "%s.account must carry the pinned wording", s.tool)
		if first == "" {
			first, firstTool = p.Description, s.tool
			continue
		}
		assert.Equalf(t, first, p.Description,
			"%s.account and %s.account must carry BYTE-IDENTICAL wording: the parameter means the same "+
				"nothing on every surface, and two phrasings of that are two claims", firstTool, s.tool)
	}

	// The sentence itself, asserted once against the shared text rather than five
	// times against five copies.
	assert.Contains(t, first, "NO BUILT-IN FAMILY IS KEYED BY ACCOUNT",
		"the shared wording must still state that the parameter keys nothing")
	assert.Contains(t, first, "Consumed by nothing today.",
		"the shared wording must still state that nothing reads it")
}

// TestAccountParam_TraverseAndDeleteClientGatesAcceptIt covers the two surfaces
// whose CLIENT-side handling of `account` is the undeclared-parameter gate alone:
// traverse (the client owns rendering, the server serves the walk) and delete
// (the one write the mutate interceptors never see). Both must decline the call
// identically with and without the field, so the chain continues to the server
// unchanged — which is where the selector gate then ignores it.
func TestAccountParam_TraverseAndDeleteClientGatesAcceptIt(t *testing.T) {
	type gate func(kgtools.CallToolParams) (bool, kgtools.ToolResult)

	gates := []struct {
		tool string
		run  gate
		base string
	}{
		{"traverse", func(p kgtools.CallToolParams) (bool, kgtools.ToolResult) {
			return InterceptTraverseParams(opCtx(), rejectProbeDeps(), p)
		}, `{"start":"probe","graph":"code","repo":"knowledge"`},
		{"delete", func(p kgtools.CallToolParams) (bool, kgtools.ToolResult) {
			return InterceptDeleteGuard(opCtx(), rejectProbeDeps(), p)
		}, `{"ids":["probe"],"graph":"code","repo":"knowledge"`},
	}

	for _, g := range gates {
		t.Run(g.tool, func(t *testing.T) {
			handledWithout, resWithout := g.run(kgtools.CallToolParams{
				Name: g.tool, Arguments: json.RawMessage(g.base + `}`),
			})
			require.Falsef(t, handledWithout,
				"%s must decline a clean payload so the chain continues; it claimed it: %s",
				g.tool, toolResultText(resWithout))

			handledWith, resWith := g.run(kgtools.CallToolParams{
				Name: g.tool, Arguments: json.RawMessage(g.base + `,"account":"` + accountProbeValue + `"}`),
			})
			assert.Equalf(t, handledWithout, handledWith,
				"%s must decline identically with the ignored `account` field set", g.tool)
			assert.Equalf(t, toolResultText(resWithout), toolResultText(resWith),
				"%s must answer byte-identically with and without `account`", g.tool)
		})
	}

	// KNOWN-POSITIVE CONTROL for both gates: an UNDECLARED key is still refused,
	// so the acceptances above are the gate running rather than the gate absent.
	t.Run("both gates still refuse an undeclared key", func(t *testing.T) {
		for _, g := range gates {
			handled, res := g.run(kgtools.CallToolParams{
				Name: g.tool, Arguments: json.RawMessage(g.base + `,"acccount":"typo"}`),
			})
			require.Truef(t, handled, "%s must claim a payload carrying an undeclared key", g.tool)
			assert.Containsf(t, toolResultText(res), "acccount",
				"%s's refusal must name the undeclared key", g.tool)
		}
	})
}

// TestAccountParam_SearchResultIsIdenticalWithAccount is requirement 3's
// BEHAVIORAL row for the search surface, which had only the wording row.
//
// THE NAIVE PAIR IS NOT AN INSTRUMENT HERE, which is why this row is built the
// way it is. Driving InterceptSearch under the package's bare stub deps returns
// the same early infrastructure refusal on both sides ("client segment engine
// unavailable"), and two identical refusals compare equal — a pin written that
// way reports green while observing nothing. So this drives the arm with a
// seeded segment engine and a wire handler, produces a REAL hydrated result, and
// reads the known-positive BEFORE the comparison.
//
// IT COMPARES BOTH HALVES OF THE CALL. The rendered text is what a caller sees;
// the captured Execute requests are what the server is asked for. A fix that kept
// the render identical while sending a different Target would pass a render-only
// pin, and the account field's whole history on this surface is a value that
// traveled further than the render showed.
//
// WHY NOT THE SERVER HANDLER HARNESS, which is where the query and traverse seam
// test runs: there is no server-side search to drive. A registered-custom search
// is client-served end to end — composeRegisteredGraphSearch runs the client
// segment engine and the only wire call is the ids[] hydrate — so the client path
// with its wire traffic captured IS the full venue for this surface.
func TestAccountParam_SearchResultIsIdenticalWithAccount(t *testing.T) {
	drive := func(args map[string]any) (string, []*knowledgev1.ExecuteRequest, *fakeSegmentSearcher) {
		var execHits, embedCalls atomic.Int64
		gc, handler := newInterceptHarnessWithHandler(t, &execHits, cannedNodesResp(
			&knowledgev1.Node{Id: "h1", Type: "fact", SymbolName: "HelloWorld"},
		))
		handler.graphNames = []string{"demo"}
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "h1", Score: 0.9}}}
		deps := &interceptDeps{
			gc: gc, emb: stubEmbedder{calls: &embedCalls}, segMgr: mgr,
			gtCRUD: registeredGraphTypes("hellograph"),
		}
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, args))
		require.True(t, handled, "the search must be claimed client-side")
		require.False(t, out.IsError, "the search must serve, not refuse: %s", engine.FirstTextContent(out))
		return engine.FirstTextContent(out), handler.recordedReqs(), mgr
	}

	// format:"json" IS LOAD-BEARING, not a preference. The markdown render drops
	// the per-row GraphInstance, and that field is exactly where the account value
	// used to land through the hydrate selector — a text-rendered pair stays green
	// while every row is stamped with a graph the results did not come from.
	// MEASURED: with the pivot defect shape planted on this arm, the text pair
	// passed and the json pair failed.
	base := map[string]any{"graph": "hellograph", "name": "demo", "query": "world", "format": "json"}
	withAcct := map[string]any{
		"graph": "hellograph", "name": "demo", "query": "world", "format": "json",
		"account": accountProbeValue,
	}

	bodyWithout, reqsWithout, mgrWithout := drive(base)
	bodyWith, reqsWith, mgrWith := drive(withAcct)

	// KNOWN POSITIVE, read before the comparison: the base call produced an actual
	// hydrated result set, so "identical" is a statement about two results rather
	// than about two refusals or two empties.
	require.Contains(t, bodyWithout, "HelloWorld",
		"the base search must render a hydrated hit, or this pair compares two non-results")
	require.Contains(t, bodyWithout, "demo",
		"the base body must carry the instance the results came from, or the row that "+
			"catches an account-stamped instance is reading a field the render omits")
	require.Equal(t, int64(1), mgrWithout.calls.Load(), "the client segment engine must have run")
	require.NotEmpty(t, reqsWithout, "the base search must have issued its hydrate read")

	assert.Equal(t, bodyWithout, bodyWith,
		"search must render byte-identically with the ignored `account` parameter set")

	// AND THE INSTANCE KEY THE CLIENT ENGINE WAS DRIVEN ON, which is the field
	// `account` used to be able to displace through the hydrate selector.
	assert.Equal(t, mgrWithout.lastName, mgrWith.lastName,
		"the segment engine must be keyed on the SAME instance with and without `account`")
	assert.Equal(t, "demo", mgrWith.lastName,
		"the instance is the one `name` selects — an account value must never become it")

	// AND THE WIRE TRAFFIC.
	require.Len(t, reqsWith, len(reqsWithout),
		"the same search must issue the same number of Execute calls with and without `account`")
	for i := range reqsWithout {
		assert.Truef(t, proto.Equal(reqsWithout[i], reqsWith[i]),
			"Execute request %d must be identical with and without `account`", i)
	}
}

// TestMutateArmRegistry_AccountIsAcceptedAndIgnoredOnEveryArm is requirement 3
// AS AMENDED. It replaced a pin that asserted mutate's per-arm partition was
// UNCHANGED, and the replacement is the point rather than a widening.
//
// WHAT THE PIN WAS PINNING. The venue D run measured one mutate shape and read
// it as accept-and-ignore for the surface. It is not: armNonKnowledgeFallthrough
// CONSUMES the field, while the knowledge-graph create / update / link arms
// REJECT it by name, so a mutate at the knowledge graph carrying `account` errors
// before any write. A test asserting that partition holds is a test that pins the
// defect in place — the exact shape a green suite uses to keep one.
//
// THE RULING IS SURFACE-WIDE: "Keep the `account` parameter on the five tool
// surfaces that still advertise it, accepted and ignored". mutate is one of the
// five, so a refusal there is the same defect on a third surface, not a local
// behavior to preserve.
//
// THE TWO ADMISSIBLE CLASSES ARE THE SAME AS ON THE QUERY SURFACE. An arm that
// CONSUMES the field routes it to a server that ignores it, which is an accurate
// statement about the client and produces no refusal. An arm that DELIBERATELY
// IGNORES it drops it with a justification. REJECTED is the class the ruling
// forbids.
func TestMutateArmRegistry_AccountIsAcceptedAndIgnoredOnEveryArm(t *testing.T) {
	require.NotEmpty(t, mutateArmRegistry, "the mutate registry must be assembled")

	ignoring, consuming := 0, 0
	for _, arm := range sortedMutateArmIDs() {
		spec := mutateArmRegistry[arm]
		t.Run(string(arm), func(t *testing.T) {
			class, ok := registryParamClass(mutateArmRegistry, arm, "account")
			require.Truef(t, ok, "mutate arm %s must classify `account`", arm)
			assert.Falsef(t, spec.rejected["account"],
				"mutate arm %s must NOT reject `account`: the owner's ruling covers all five surfaces "+
					"that advertise the parameter, and mutate is one of them", arm)

			switch class {
			case classDeliberatelyIgnored:
				ignoring++
				assert.NotEmptyf(t, spec.deliberatelyIgnored["account"],
					"mutate arm %s must justify ignoring `account`", arm)
			case classConsumed:
				consuming++
			case classRejected:
				t.Fatalf("mutate arm %s rejects `account`, which the ruling forbids", arm)
			}
		})
	}

	assert.NotZero(t, ignoring, "no mutate arm ignores `account` — the ruling's applier did not run")
	assert.Truef(t, consuming > 0 && mutateArmRegistry[armNonKnowledgeFallthrough].consumed["account"],
		"armNonKnowledgeFallthrough must still CONSUME `account`: it is the arm that declines to the "+
			"engine where the Target is built, and demoting it would be a false statement about the client")

	// `repo` IS THE UNTOUCHED CONTROL, and it is what proves this change is
	// scoped to one parameter rather than a general relaxation of the mutate
	// gate. It sits beside `account` in the same rejected sets, it names a
	// family a caller CAN address, and no ruling covers it — so it must still be
	// rejected on the arms that reject it.
	t.Run("repo is still rejected on the knowledge-graph arms", func(t *testing.T) {
		assert.Truef(t, mutateArmRegistry[armGraphPassthrough].rejected["repo"],
			"armGraphPassthrough must still reject `repo` — only `account` is ruled accepted and ignored")
	})
}

// TestAccountMutateParams_AccountDoesNotChangeTheGateAnswer is the behavioral
// half of the amended requirement 3, driven through the gate every mutate arm
// calls before any write. The registry cell above says what the table declares;
// this says what a caller gets.
func TestAccountMutateParams_AccountDoesNotChangeTheGateAnswer(t *testing.T) {
	for _, arm := range sortedMutateArmIDs() {
		t.Run(string(arm), func(t *testing.T) {
			without := gateAnswer(accountMutateParams(arm, mutateArgs{raw: payloadWithout}))
			require.Emptyf(t, without,
				"the empty payload must pass arm %s's gate, or this pair cannot tell an ignored "+
					"account from a refusal the call was already getting", arm)

			with := gateAnswer(accountMutateParams(arm, mutateArgs{raw: payloadWithAccnt}))
			assert.Equalf(t, without, with,
				"mutate arm %s must answer identically with the ignored `account` field set, "+
					"and it answered %q", arm, with)
		})
	}

	// KNOWN-POSITIVE CONTROL, and it is `repo` rather than an arbitrary param on
	// purpose: repo is the field that sat beside account in the same rejected
	// sets, so its surviving refusal proves the gate still runs on the very
	// payload shape the rows above now pass.
	t.Run("the gate still refuses repo on the arms that reject it", func(t *testing.T) {
		refused := 0
		for _, arm := range sortedMutateArmIDs() {
			if err := accountMutateParams(arm, mutateArgs{raw: json.RawMessage(`{"repo":"knowledge"}`)}); err != nil {
				refused++
				assert.Containsf(t, err.Error(), "repo", "arm %s's refusal must name the offending field", arm)
			}
		}
		require.NotZero(t, refused,
			"no mutate arm refused `repo` — the gate is inert and every acceptance above is vacuous")
	})
}

// sortedMutateArmIDs orders the mutate registry's arms so a failure names the
// same arm first on every run, the way sortedArmIDs does for the query surface.
func sortedMutateArmIDs() []armID {
	arms := make([]armID, 0, len(mutateArmRegistry))
	for arm := range mutateArmRegistry {
		arms = append(arms, arm)
	}
	slices.Sort(arms)
	return arms
}

// TestDeleteReadKeys_StillCreditAccount is requirement 3 for the delete surface:
// `account` stays part of the delete tool's locked read-key set, so the fix
// cannot quietly drop the field from the one write the mutate interceptors never
// see.
func TestDeleteReadKeys_StillCreditAccount(t *testing.T) {
	assert.Contains(t, deleteReadKeys, "account",
		"delete's locked read-key set must still carry `account`; the two-sided handshake with "+
			"package engine is what keeps the wire field alive on this surface")
}
