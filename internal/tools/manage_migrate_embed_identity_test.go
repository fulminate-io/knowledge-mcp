// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestManageSchema_MigrateEmbedIdentityOperation is the REGISTRATION pin, and it
// exists because two of the four registration sites are load-bearing in a way
// unit tests cannot otherwise see.
//
// InterceptManage calls rejectUndeclaredParams FIRST. An operation whose params
// are absent from the published InputSchema is therefore UNREACHABLE in
// production — the request is rejected before the handler is ever entered —
// while every test that calls the handler directly still passes. So the params
// are pinned here, not just the enum.
//
// Modeled on TestAstSchema_ReplaceOperation, which pins the same two things for
// the ast tool and passes today, so this shape is known satisfiable rather than
// merely plausible.
func TestManageSchema_MigrateEmbedIdentityOperation(t *testing.T) {
	def := ManageToolDef()

	t.Run("operation_enum_advertises_the_op", func(t *testing.T) {
		opProp, ok := def.InputSchema.Properties["operation"]
		require.True(t, ok, "the manage tool must publish an operation property")
		assert.Contains(t, opProp.Enum, "migrate_embed_identity",
			"the operation enum must advertise the op, or no caller can name it")

		// KNOWN-POSITIVE, same run: an operation that IS reachable today is in the
		// same enum, so a failure above is about this op rather than about the enum
		// being read wrongly.
		assert.Contains(t, opProp.Enum, "promote_metadata")

		assert.Contains(t, def.Description, "migrate_embed_identity",
			"the tool description must document the operation, as it does for the others")
	})

	t.Run("graph_property_declared", func(t *testing.T) {
		_, ok := def.InputSchema.Properties["graph"]
		assert.True(t, ok,
			"InputSchema must advertise the graph property — the operation migrates ONE named graph, "+
				"and rejectUndeclaredParams runs BEFORE the handler")
		_, ok = def.InputSchema.Properties["name"]
		assert.True(t, ok, "and the name that identifies it within that graph type")
	})

	t.Run("profile_property_declared", func(t *testing.T) {
		_, ok := def.InputSchema.Properties["profile"]
		assert.True(t, ok,
			"InputSchema must advertise the profile property — rejectUndeclaredParams runs BEFORE the "+
				"handler, so an undeclared param makes the operation unreachable in production while "+
				"every direct-handler test still passes")
	})
}

// TestManageOperations_MigrateEmbedIdentityListed pins the canonical list, which
// TestUnknownOperationLists_MatchDeclaredSchemas holds SET-EQUAL with the enum
// above. Landing one without the other reds that test — which is the desired
// behaviour and the reason both are asserted here.
func TestManageOperations_MigrateEmbedIdentityListed(t *testing.T) {
	assert.True(t, slices.Contains(manageOperations, "migrate_embed_identity"),
		"the canonical operation list must carry the op, or an unknown-operation error names it as invalid")
	assert.True(t, slices.IsSorted(manageOperations),
		"the list is sorted; an insertion that breaks the order makes the next one harder to place")
}

// TestManageMigrateEmbedIdentity pins the migrate duties observable at the
// CLIENT surface of the operation.
//
// THE OTHER LEGS ARE SERVER-SIDE AND LIVE IN THE SERVER MODULE. The embed cache
// and the identity record are both server-resident, and cmd/knowledge may not
// import cmd/knowledge-server — the two are separate modules whose only shared
// contract is generated protobuf. The write ORDER, the shared transaction and
// the prune SCOPE are therefore pinned in
// cmd/knowledge-server/internal/bootstrap/engine_mutate_migrate_identity_test.go
// under a test of this same name.
func TestManageMigrateEmbedIdentity(t *testing.T) {
	// NO DEFAULT-ON-MISS. A migration that quietly used the default profile would
	// record an identity the operator did not choose — and the record is
	// authoritative afterwards, so that wrong choice survives until someone runs
	// another migration. The refusal names the DEFINED profiles because the set is
	// the operator's own: a typo is otherwise indistinguishable from a profile
	// they forgot to write.
	t.Run("unknown_profile_refused", func(t *testing.T) {
		deps, hits := migrateHarness(t)

		res := handleMigrateEmbedIdentity(opCtx(), deps, manageArgs{
			Graph: "code", Name: "myrepo", Profile: "code5",
		})
		require.True(t, res.IsError, "an unknown profile must be refused, never defaulted")
		body := res.Content[0].Text
		assert.Contains(t, body, "code5", "the refusal names the value it rejected")
		assert.Contains(t, body, "code4", "and lists the profiles that ARE defined")
		require.Equal(t, int64(0), hits.Load(), "the refused migration must not have reached the wire")

		// KNOWN-POSITIVE, same run: a DEFINED profile resolves and the migration
		// runs to completion. Without it, a handler that refused EVERY profile
		// would satisfy the assertions above.
		res = handleMigrateEmbedIdentity(opCtx(), deps, manageArgs{
			Graph: "code", Name: "myrepo", Profile: "code4",
		})
		require.False(t, res.IsError, "a DEFINED profile must be accepted: %s", res.Content[0].Text)
		assert.Positive(t, hits.Load(), "and it drives the wire")
	})

	// COST-ANNOUNCED IS A SPECIFIED BEHAVIOUR. The decision's explicit-spend spine
	// has two halves: the spend is explicit in the ARCHITECTURE (only this op can
	// trigger it) and explicit to the OPERATOR (they see what it costs and what
	// changed). An implementation that silently did the right thing fails here.
	t.Run("result_reports_node_count_and_transition", func(t *testing.T) {
		deps, _ := migrateHarness(t)

		res := handleMigrateEmbedIdentity(opCtx(), deps, manageArgs{
			Graph: "code", Name: "myrepo", Profile: "code4",
		})
		require.False(t, res.IsError, res.Content[0].Text)
		body := res.Content[0].Text

		assert.Contains(t, body, "\"vectors_cleared\":7",
			"the result reports how many vectors were cleared — the nodes about to be re-embedded")
		assert.Contains(t, body, "re-embedded",
			"and says what that count means, so the operator does not have to infer the bill")

		// BOTH ENDS OF THE TRANSITION. A destination alone cannot tell a no-op
		// migration from a corpus-scale one.
		assert.Contains(t, body, "\"identity_from\":\"voyage/voyage-code-3 at 1024 ubinary\"",
			"the identity being SUPERSEDED comes from the server's record")
		assert.Contains(t, body, "\"identity_to\":\"voyage/m at 1024 ubinary\"",
			"and the identity being adopted comes from the named profile")
		assert.Contains(t, body, "code4", "along with the profile that named it")
	})

	// The operation migrates ONE named graph, deliberately: a migration with an
	// implicit target is a corpus-scale embedding bill charged to whichever graph
	// happened to be current.
	t.Run("requires_graph_name_profile", func(t *testing.T) {
		deps := &interceptDeps{}
		for _, tc := range []struct {
			name string
			args manageArgs
			want string
		}{
			{"no graph", manageArgs{Name: "r", Profile: "p"}, "graph is required"},
			{"no name", manageArgs{Graph: "code", Profile: "p"}, "name is required"},
			{"no profile", manageArgs{Graph: "code", Name: "r"}, "profile is required"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				res := handleMigrateEmbedIdentity(opCtx(), deps, tc.args)
				require.True(t, res.IsError)
				assert.Contains(t, res.Content[0].Text, tc.want)
			})
		}
	})
}

// migrateHarness installs a config defining exactly one profile and a real graph
// client against a canned server that reports a PRIOR identity for the graph and
// seven cleared vectors for the migration.
//
// A REAL CLIENT, NOT A ZERO-VALUE deps: a zero-value hands back a typed-nil
// caller, which the handler's nil check cannot see and the call panics on — so a
// known-positive leg that must actually reach the wire needs this.
//
// THE PRIOR IDENTITY DIFFERS FROM THE PROFILE'S in model, so a handler that
// rendered the destination on both ends of the transition fails rather than
// reading as correct.
func migrateHarness(t *testing.T) (*interceptDeps, *atomic.Int64) {
	t.Helper()
	cfg, err := config.Parse([]byte(
		"[embedder.profile.code4]\nprovider = \"voyage\"\nmodel = \"m\"\ndimension = 1024\ndtype = \"ubinary\"\n"))
	require.NoError(t, err)
	t.Cleanup(config.SetForTest(cfg))

	var hits atomic.Int64
	// ONE canned response serves both Executes the operation makes, because the
	// two readers look at different fields: the catalog read at GraphNames, the
	// migration at AffectedCount. That is the same single-response idiom
	// cannedEmbeddedNodesResp uses, rather than a per-request router.
	gc := newInterceptHarness(t, &hits, &knowledgev1.ExecuteResponse{
		AffectedCount: 7,
		GraphNames: []*knowledgev1.GraphInfo{{
			Name:   "myrepo",
			Loaded: true,
			EmbedIdentity: &knowledgev1.EmbedIdentity{
				Provider: "voyage", Model: "voyage-code-3", Dimension: 1024, Dtype: "ubinary",
			},
		}},
	})
	return &interceptDeps{gc: gc}, &hits
}
