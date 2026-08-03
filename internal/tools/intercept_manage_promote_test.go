// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// metadataStatsResp builds a MetadataStatsResponse carrying the given typed
// MetadataStats keys + override config, exactly as the server's MetadataStats
// RPC does (both typed carriers). Used to seed the promote_metadata composer's
// stats read — the composer reads resp.GetMetadataStats() / GetOverrideConfig()
// straight off the response (no decode).
func metadataStatsResp(_ *testing.T, keys map[string]*knowledgev1.KeyStats, cfg *knowledgev1.OverrideConfig) *knowledgev1.MetadataStatsResponse {
	return &knowledgev1.MetadataStatsResponse{
		MetadataStats:  &knowledgev1.MetadataStats{Keys: keys},
		OverrideConfig: cfg,
	}
}

// TestHandleManagePromoteMetadata_RejectsDisallowedGraph covers that
// the parseMetadataGraphTypeForBackfill code/knowledge/linkage/empty
// rejections are applied CLIENT-SIDE before any stats read or MIGRATE_META_REPR
// dispatch — a disallowed graph returns the operator-facing rejection without
// touching the server (no metadata_stats / mutate call recorded).
func TestHandleManagePromoteMetadata_RejectsDisallowedGraph(t *testing.T) {
	for _, tc := range []struct{ graph, wantMsg string }{
		{"", "empty graph parameter"},
		{"code", "code graphs use the T6 path"},
		{"knowledge", "knowledge graph is out of scope"},
		{"linkage", "linkage graph carries no promotable metadata"},
	} {
		fc := &fakeGraphCaller{}
		res := handleManagePromoteMetadata(context.Background(), interceptTestDeps{gc: fc},
			manageArgs{Operation: "promote_metadata", Graph: tc.graph, Name: "x"},
			json.RawMessage(`{"operation":"promote_metadata","graph":"`+tc.graph+`","name":"x"}`))
		require.True(t, res.IsError, "graph=%q must be rejected", tc.graph)
		assert.Contains(t, toolResultText(res), tc.wantMsg)
		assert.Empty(t, fc.calls, "graph=%q rejection must not touch the server", tc.graph)
	}
}

// TestParseMetadataGraphTypeForBackfill_RegisteredCustom is the fails-when-absent
// guard for the registered-custom default arm: a registered custom graph type
// (fake ByName→found) is ACCEPTED, while an unregistered typo (ByName→not found)
// is REJECTED with the 'unsupported graph' message. Reverting the registered-
// custom arm makes the acceptance case FAIL (the type would reject as unsupported).
// Builtin acceptance/rejection is covered by the sibling tests, unchanged.
func TestParseMetadataGraphTypeForBackfill_RegisteredCustom(t *testing.T) {
	ctx := context.Background()
	crud := &fakeGraphTypeCRUD{graph: map[string]*knowledgev1.GraphTypeDef{
		"hellograph": {Name: "hellograph"},
	}}

	t.Run("registered custom type is accepted", func(t *testing.T) {
		gt, err := parseMetadataGraphTypeForBackfill(ctx, crud, "hellograph")
		require.NoError(t, err, "a registered custom graph type must be accepted, not rejected as unsupported")
		assert.Equal(t, kgtypes.GraphType("hellograph"), gt)
	})

	t.Run("unregistered typo is rejected", func(t *testing.T) {
		_, err := parseMetadataGraphTypeForBackfill(ctx, crud, "hellogarph")
		require.Error(t, err, "an unregistered custom graph type must be rejected")
		assert.Contains(t, err.Error(), "unsupported graph")
	})

	t.Run("nil crud rejects custom (degraded client)", func(t *testing.T) {
		_, err := parseMetadataGraphTypeForBackfill(ctx, nil, "hellograph")
		require.Error(t, err, "with no registry available, a custom type cannot be confirmed and is rejected")
		assert.Contains(t, err.Error(), "unsupported graph")
	})

	t.Run("builtins still resolve without consulting the registry", func(t *testing.T) {
		gt, err := parseMetadataGraphTypeForBackfill(ctx, nil, "cloud")
		require.NoError(t, err)
		assert.Equal(t, kgtypes.GraphCloud, gt)
	})
}

// TestHandleManagePromoteMetadata_PromoteAndDemote covers that
// the composer reads metadata_stats, computes RecommendAction per key, and
// dispatches one MIGRATE_META_REPR per promote/demote key (edge / scalar
// direction), skipping KEEP keys. No promote_metadata gc.Call is made.
func TestHandleManagePromoteMetadata_PromoteAndDemote(t *testing.T) {
	fc := &fakeGraphCaller{
		// scalar + low distinct + median>=5 → PROMOTE (edge); edge + high distinct
		// → DEMOTE (scalar); scalar + high distinct → KEEP (scalar, skipped).
		metadataStatsResp: metadataStatsResp(t, map[string]*knowledgev1.KeyStats{
			"team":     {DistinctValues: 4, MedianNodesPerValue: 10, CurrentRepresentation: engine.RepresentationScalar},
			"trace_id": {DistinctValues: 9000, MedianNodesPerValue: 1, CurrentRepresentation: engine.RepresentationEdge},
			"kept":     {DistinctValues: 9000, MedianNodesPerValue: 1, CurrentRepresentation: engine.RepresentationScalar},
		}, &knowledgev1.OverrideConfig{}),
	}
	res := handleManagePromoteMetadata(context.Background(), interceptTestDeps{gc: fc},
		manageArgs{Operation: "promote_metadata", Graph: "cloud", Name: "acct-1"},
		json.RawMessage(`{"operation":"promote_metadata","graph":"cloud","name":"acct-1"}`))
	require.False(t, res.IsError, "promote: %s", toolResultText(res))

	// Exactly two MIGRATE_META_REPR dispatches: team→edge, trace_id→scalar. kept
	// is KEEP → no dispatch. (The non-dry-run narrative think() also fires CREATE/
	// LINK mutations; filter to the migration kind only.)
	var promoteKeys, demoteKeys []string
	for _, m := range fc.execMutations {
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_MIGRATE_META_REPR {
			continue
		}
		spec := m.GetMigrateMetaRepr()
		switch spec.GetTargetRepr() {
		case knowledgev1.MigrateMetaReprSpec_TARGET_REPR_EDGE:
			promoteKeys = append(promoteKeys, spec.GetMetadataKey())
		case knowledgev1.MigrateMetaReprSpec_TARGET_REPR_SCALAR:
			demoteKeys = append(demoteKeys, spec.GetMetadataKey())
		}
	}
	assert.Equal(t, []string{"team"}, promoteKeys, "PROMOTE → one EDGE migration")
	assert.Equal(t, []string{"trace_id"}, demoteKeys, "DEMOTE → one SCALAR migration")

	// No promote_metadata gc.Call (the whole op is client-composed).
	for _, c := range fc.calls {
		assert.NotEqual(t, "manage", c.tool, "no manage(promote_metadata) gc.Call")
	}

	body := toolResultText(res)
	assert.Contains(t, body, "Promotion pass on cloud/acct-1")
	assert.Contains(t, body, "PROMOTE: team")
	assert.Contains(t, body, "DEMOTE: trace_id")
}

// TestHandleManagePromoteMetadata_DryRunSkipsDispatch covers that
// dry_run computes the decisions (and renders them) but issues NO
// MIGRATE_META_REPR dispatch and NO narrative think().
func TestHandleManagePromoteMetadata_DryRunSkipsDispatch(t *testing.T) {
	fc := &fakeGraphCaller{
		metadataStatsResp: metadataStatsResp(t, map[string]*knowledgev1.KeyStats{
			"team": {DistinctValues: 4, MedianNodesPerValue: 10, CurrentRepresentation: engine.RepresentationScalar},
		}, &knowledgev1.OverrideConfig{}),
	}
	res := handleManagePromoteMetadata(context.Background(), interceptTestDeps{gc: fc},
		manageArgs{Operation: "promote_metadata", Graph: "cloud", Name: "x", DryRun: true},
		json.RawMessage(`{"operation":"promote_metadata","graph":"cloud","name":"x","dry_run":true}`))
	require.False(t, res.IsError)

	assert.Empty(t, fc.execMutations, "dry_run must not dispatch MIGRATE_META_REPR or the narrative think")
	body := toolResultText(res)
	assert.Contains(t, body, "dry_run=true")
	assert.Contains(t, body, "PROMOTE: team", "dry_run still shows the decision")
}

// TestHandleManagePromoteMetadata_KeysFilter covers the params["keys"] override:
// only the named keys are considered (not the full stats snapshot).
func TestHandleManagePromoteMetadata_KeysFilter(t *testing.T) {
	fc := &fakeGraphCaller{
		metadataStatsResp: metadataStatsResp(t, map[string]*knowledgev1.KeyStats{
			"team":  {DistinctValues: 4, MedianNodesPerValue: 10, CurrentRepresentation: engine.RepresentationScalar},
			"other": {DistinctValues: 4, MedianNodesPerValue: 10, CurrentRepresentation: engine.RepresentationScalar},
		}, &knowledgev1.OverrideConfig{}),
	}
	res := handleManagePromoteMetadata(context.Background(), interceptTestDeps{gc: fc},
		manageArgs{Operation: "promote_metadata", Graph: "cloud", Name: "x"},
		json.RawMessage(`{"operation":"promote_metadata","graph":"cloud","name":"x","keys":"team"}`))
	require.False(t, res.IsError)

	// Only the keys-filtered 'team' key is migrated (the non-dry-run narrative
	// think() also fires; filter to the migration kind).
	var migrated []string
	for _, m := range fc.execMutations {
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_MIGRATE_META_REPR {
			migrated = append(migrated, m.GetMigrateMetaRepr().GetMetadataKey())
		}
	}
	assert.Equal(t, []string{"team"}, migrated, "only the keys-filtered 'team' key is migrated")
}

// TestPromoteMetadataNarrative_NoForeignTicketLink covers the batch-level
// narrative think() on a non-dry-run with keys processed: a thought CREATE joins
// the metadata-backfill session (EdgeKGContains, riding the create_batch
// atomically per the Phase-1 think-path fix) and carries NO relates-to edge out
// of the thought. Session membership is the only anchor a consumer's graph can
// resolve; any node id minted at this call site belongs to a different graph and
// would dangle in every one it is shipped to.
func TestPromoteMetadataNarrative_NoForeignTicketLink(t *testing.T) {
	fc := &fakeGraphCaller{
		metadataStatsResp: metadataStatsResp(t, map[string]*knowledgev1.KeyStats{
			"team": {DistinctValues: 4, MedianNodesPerValue: 10, CurrentRepresentation: engine.RepresentationScalar},
		}, &knowledgev1.OverrideConfig{}),
		mutateIDs: []string{"sess-or-thought-id"},
	}
	res := handleManagePromoteMetadata(context.Background(), interceptTestDeps{gc: fc},
		manageArgs{Operation: "promote_metadata", Graph: "cloud", Name: "x"},
		json.RawMessage(`{"operation":"promote_metadata","graph":"cloud","name":"x"}`))
	require.False(t, res.IsError)

	// The session ("metadata-backfill") is not pre-seeded, so getOrCreateThoughtSessionClient
	// CREATES it first (PersistBatch returns "sess-or-thought-id"); the narrative
	// thought's CREATE batch then carries session--contains-->thought from that id.
	var thoughtBody *knowledgev1.NodeBody
	sawContains, sawThoughtRelatesTo := false, false
	for _, m := range fc.execMutations {
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			continue
		}
		for _, b := range m.GetNodeBodies() {
			if b.GetType() == "thought" {
				thoughtBody = b
			}
		}
		// The session contains edge now rides the CREATE batch (hasContainsFrom).
		if hasContainsFrom(m, "sess-or-thought-id") {
			sawContains = true
		}
		// Any relates-to out of the narrative thought (slot 0) is a violation. The
		// check names no id at all, so it cannot be satisfied by swapping one
		// hardcoded target for another.
		for _, e := range m.GetEdges() {
			if e.GetType() == string(kgtypes.EdgeRelatesTo) && e.GetFromIdx() == 0 {
				sawThoughtRelatesTo = true
			}
		}
	}
	require.NotNil(t, thoughtBody, "narrative must create a type:thought node")
	assert.Contains(t, thoughtBody.GetContent(), "Backfill on cloud/x: refreshed 1 keys")
	assert.Equal(t, "metadata-backfill", thoughtBody.GetMetadata()["session"])
	assert.True(t, sawContains, "narrative thought must join the metadata-backfill session via the EdgeKGContains batch edge on the CREATE plan")
	assert.False(t, sawThoughtRelatesTo, "narrative thought must carry no relates-to edge out of it — a link minted here dangles in every consumer graph")
}

// TestHandleManagePromoteMetadata_StatsLoadFailure covers the stats-read error
// path: a MetadataStats RPC failure surfaces as an operator-facing error before
// any dispatch.
func TestHandleManagePromoteMetadata_StatsLoadFailure(t *testing.T) {
	fc := &fakeGraphCaller{metadataStatsErr: errors.New("connection refused")}
	res := handleManagePromoteMetadata(context.Background(), interceptTestDeps{gc: fc},
		manageArgs{Operation: "promote_metadata", Graph: "cloud", Name: "x"},
		json.RawMessage(`{"operation":"promote_metadata","graph":"cloud","name":"x"}`))
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "load stats failed")
	assert.Empty(t, fc.execMutations, "no dispatch when the stats read fails")
}

// TestHandleManagePromoteMetadata_ForceOverridesDispatch covers the OverrideConfig
// carrier threading: an operator-pinned ForceEdge key dispatches an EDGE
// migration and a ForceScalar key dispatches a SCALAR migration, REGARDLESS of
// the live cardinality (the force precedence in store.RecommendAction). Proves
// the second carrier (override config) is decoded + threaded into the decision.
func TestHandleManagePromoteMetadata_ForceOverridesDispatch(t *testing.T) {
	fc := &fakeGraphCaller{
		// pinned_edge: live data says KEEP (high distinct, scalar) but ForceEdge
		// pins it → FORCE_EDGE → edge migration. pinned_scalar: live data says
		// KEEP (low distinct, edge) but ForceScalar pins it → FORCE_SCALAR → scalar.
		metadataStatsResp: metadataStatsResp(t, map[string]*knowledgev1.KeyStats{
			"pinned_edge":   {DistinctValues: 9000, MedianNodesPerValue: 1, CurrentRepresentation: engine.RepresentationScalar},
			"pinned_scalar": {DistinctValues: 4, MedianNodesPerValue: 10, CurrentRepresentation: engine.RepresentationEdge},
		}, &knowledgev1.OverrideConfig{ForceEdge: []string{"pinned_edge"}, ForceScalar: []string{"pinned_scalar"}}),
	}
	res := handleManagePromoteMetadata(context.Background(), interceptTestDeps{gc: fc},
		manageArgs{Operation: "promote_metadata", Graph: "cloud", Name: "x"},
		json.RawMessage(`{"operation":"promote_metadata","graph":"cloud","name":"x"}`))
	require.False(t, res.IsError, "force: %s", toolResultText(res))

	dir := map[string]knowledgev1.MigrateMetaReprSpec_TargetRepr{}
	for _, m := range fc.execMutations {
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_MIGRATE_META_REPR {
			spec := m.GetMigrateMetaRepr()
			dir[spec.GetMetadataKey()] = spec.GetTargetRepr()
		}
	}
	assert.Equal(t, knowledgev1.MigrateMetaReprSpec_TARGET_REPR_EDGE, dir["pinned_edge"], "ForceEdge → EDGE migration")
	assert.Equal(t, knowledgev1.MigrateMetaReprSpec_TARGET_REPR_SCALAR, dir["pinned_scalar"], "ForceScalar → SCALAR migration")
}

// TestHandleManagePromoteMetadata_NameRequired covers the missing-name guard.
func TestHandleManagePromoteMetadata_NameRequired(t *testing.T) {
	fc := &fakeGraphCaller{}
	res := handleManagePromoteMetadata(context.Background(), interceptTestDeps{gc: fc},
		manageArgs{Operation: "promote_metadata", Graph: "cloud"},
		json.RawMessage(`{"operation":"promote_metadata","graph":"cloud"}`))
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "name=<graph identifier> is required")
	assert.Empty(t, fc.calls, "name guard fires before any server touch")
}
