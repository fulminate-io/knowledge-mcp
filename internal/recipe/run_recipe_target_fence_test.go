// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// targetFenceCaller seeds a routingCaller whose recipe node carries an arbitrary
// target_graph_type, so a full RunRecipe can be driven against an arbitrary
// declared target. The recipe body and source rows are the same trivial
// select→emit shape simpleEmitCaller uses: a run that is NOT fenced emits two
// pattern nodes and ships exactly one CollectResult.
func targetFenceCaller(targetGT string) *routingCaller {
	return &routingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphTransformers): {{
				Id: "rec-fence", Type: "recipe", SymbolName: "eip", Content: simpleEmitRecipeBody, UpdatedAt: 1,
				Metadata: map[string]string{
					"source_graph_type": string(kgtypes.GraphWebRaw),
					"target_graph_type": targetGT,
					"target_name":       "knowledge",
				},
			}},
			string(kgtypes.GraphWebRaw): {
				{Id: "s1", Type: "section", SymbolName: "Message Router"},
				{Id: "s2", Type: "section", SymbolName: "Message Channel"},
			},
		},
		edgesByGraph: map[string][]*knowledgev1.Edge{},
	}
}

// TestRunRecipe_RefusesCollectorOwnedTargetGraphType is the target-fence test.
//
// The defect: RunRecipe built TargetSpec straight from the recipe node's
// target_graph_type metadata with no validation, so a recipe authored with
// target_graph_type "code" produced a full-replace collect against a real code
// graph — whose epoch sweep removes everything the recipe did not emit.
//
// This test also settles end-to-end reachability EMPIRICALLY rather than by
// inspection: it drives the real RunRecipe entry
// point, and the "sink never called" assertion is what fails against unfixed
// code — with the captured CollectResult carrying GraphType "code". A recipe run
// COULD reach writeResult with a builtin collector-owned type; nothing between
// the recipe metadata and the sink narrowed it. See the reachability subtest
// below, which pins the intervening path.
//
// Each collector-owned type gets its own subtest so a fence that covers only
// some of them cannot pass.
func TestRunRecipe_RefusesCollectorOwnedTargetGraphType(t *testing.T) {
	collectorOwned := []kgtypes.GraphType{
		kgtypes.GraphCode,
		kgtypes.GraphCloud,
		kgtypes.GraphCICD,
		kgtypes.GraphLogs,
		kgtypes.GraphWebRaw,
		kgtypes.GraphPDFRaw,
	}
	for _, gt := range collectorOwned {
		t.Run(string(gt), func(t *testing.T) {
			caller := targetFenceCaller(string(gt))
			sink := &captureSink{}
			// Force:true so the pre-interpret forceDeleteBySource path is armed
			// too — the fence must precede the target DELETE, not just the write.
			opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip"), Force: true}

			_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)

			require.Error(t, err, "a recipe declaring a collector-owned target_graph_type must be REFUSED")
			assert.Contains(t, err.Error(), string(gt), "the error names the refused target graph type")
			assert.Contains(t, err.Error(), "eip", "the error names the offending recipe")
			assert.Zero(t, sink.calls,
				"the run must be refused BEFORE writeResult — a shipped CollectResult is a full-replace collect against a collector graph")
		})
	}
	// The force path gets its own SEEDED test below: asserting caller.mutations
	// is empty here would hold on unfixed code too (this fixture's target graph
	// holds no prior emissions to delete), so it would prove nothing.
}

// TestRunRecipe_ForceAgainstCollectorOwnedTarget_IssuesNoDelete pins the SECOND
// destructive channel the target fence has to close. forceDeleteBySource runs
// BEFORE Interpret, so a fence placed only around writeResult would still let a
// Force run issue a HARD DELETE against a real code graph.
//
// The fixture seeds the target code graph with this recipe's own prior
// emissions, each carrying a translated-from edge under the run's slug — the
// exact shape forceDeleteBySource dooms. Without the fence this run issues one
// MUTATION_KIND_DELETE with HardDelete:true over those ids, so the "no
// mutations" assertion here is a genuine red, not a vacuous zero.
func TestRunRecipe_ForceAgainstCollectorOwnedTarget_IssuesNoDelete(t *testing.T) {
	const slug = "hohpe-eip"
	// GraphCode is used rather than a table because it is the ticket's named
	// case and, unlike web/pdf, its target-graph key cannot collide with this
	// fixture's web source graph in routingCaller's per-graph-type routing.
	target := TargetSpec{GraphType: kgtypes.GraphCode, Name: "knowledge"}
	idRouter := StableID(TargetKey(target), slug, "pattern", "Message Router")
	idChannel := StableID(TargetKey(target), slug, "pattern", "Message Channel")

	caller := targetFenceCaller(string(kgtypes.GraphCode))
	caller.nodesByGraph[string(kgtypes.GraphCode)] = []*knowledgev1.Node{
		{Id: idRouter, Type: "pattern", SymbolName: "Message Router"},
		{Id: idChannel, Type: "pattern", SymbolName: "Message Channel"},
	}
	caller.edgesByGraph[string(kgtypes.GraphCode)] = []*knowledgev1.Edge{
		{FromId: idRouter, ToId: "s1", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor(slug)},
		{FromId: idChannel, ToId: "s2", Type: string(kgtypes.EdgeTranslatedFrom), Evidence: evidenceFor(slug)},
	}

	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest(slug, "eip"), Force: true}

	_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)

	require.Error(t, err)
	assert.Empty(t, caller.mutations,
		"the fence must precede forceDeleteBySource — no DELETE may be issued against a collector-owned graph")
	assert.Zero(t, sink.calls)
}

// TestRunRecipe_RefusesTransformersTarget covers the SELF-DESTRUCTION case,
// which is a separate refusal from the collector-owned fence and carries its own
// error.
//
// The transformers graph is the recipe store: it is where every recipe node,
// INCLUDING the one currently executing, lives. It is not collector-owned —
// nothing collects it, recipes are authored into it by mutate — so the
// collector-owned predicate neither does nor should cover it. But a recipe
// declaring target_graph_type "transformers" would ship a full-replace collect
// against that store, and the epoch sweep would delete the recipes themselves.
//
// The two refusals are asserted to be DISTINGUISHABLE: this one must name the
// recipe store, and must NOT be the collector-owned message. Without that
// negative assertion a single over-broad fence would satisfy both tests while
// telling the operator the wrong reason.
func TestRunRecipe_RefusesTransformersTarget(t *testing.T) {
	caller := targetFenceCaller(string(kgtypes.GraphTransformers))
	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip"), Force: true}

	_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)

	require.Error(t, err, "a recipe targeting the recipe store must be REFUSED")
	assert.Contains(t, err.Error(), "transformers", "the error names the refused target graph type")
	assert.Contains(t, err.Error(), "eip", "the error names the offending recipe")
	assert.Contains(t, err.Error(), "recipe store",
		"the transformers refusal states its own reason — the recipe store cannot be a recipe target")
	assert.NotContains(t, err.Error(), "collector-owned",
		"transformers is NOT collector-owned; it must not be refused under the collector fence's reason")
	assert.Zero(t, sink.calls,
		"the run must be refused BEFORE writeResult — a shipped CollectResult would full-replace the recipe store")
	assert.Empty(t, caller.mutations,
		"the run must be refused BEFORE forceDeleteBySource")
}

// TestRunRecipe_AllowsNonCollectorTargetGraphType is the known-positive control
// for the fence tests above: without it, a RunRecipe that errored for some
// unrelated reason (a broken fixture, an unparseable body) would satisfy every
// "refused / sink never called" assertion vacuously. These targets are neither
// collector-owned nor the recipe store, and must still run end-to-end and ship.
//
// practice is the only shipped recipe target today; knowledge and linkage are
// included because they are documented graph-to-graph transformer destinations
// and the fences deliberately do NOT block them. transformers is NOT here — it
// is the recipe store and has its own refusal above.
func TestRunRecipe_AllowsNonCollectorTargetGraphType(t *testing.T) {
	nonCollector := []kgtypes.GraphType{
		kgtypes.GraphPractice,
		kgtypes.GraphKnowledge,
		kgtypes.GraphLinkage,
	}
	for _, gt := range nonCollector {
		t.Run(string(gt), func(t *testing.T) {
			caller := targetFenceCaller(string(gt))
			sink := &captureSink{}
			opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}

			res, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)

			require.NoError(t, err, "a non-collector target must still run")
			require.Equal(t, 1, sink.calls, "the run ships exactly one CollectResult")
			assert.Equal(t, gt, sink.results[0].GraphType)
			assert.Equal(t, 2, res.Stats.NodesEmitted, "both source sections emitted — the fixture really does work")
		})
	}
}

// TestRunRecipe_CollectorOwnedTarget_ReachesWriteResultWithoutTheFence records
// the reachability answer in an executable form: YES — end-to-end reachability
// was real, and nothing on the path narrowed the target.
//
// The path, read first-hand this session:
//
//	collect type=web|pdf transformer=recipe
//	  → tools/collect.go:144 runRecipeCollect        (gates on the SOURCE type only)
//	  → tools/collect.go:241 recipe.RunRecipe(..., expectedSourceType=web|pdf, ...)
//	  → run_recipe.go: expectedSourceType is checked against source_graph_type — the
//	    TARGET is never checked
//	  → writeResult → collector.Sink.WriteResult
//	  → remote.UploadSink.WriteResult (collector/remote/sink.go:191) puts
//	    result.GraphType verbatim on CollectChunkRequest under a fresh epoch.
//
// So the ONLY gate on the whole path is the one this ticket adds. This test pins
// the two properties that made it reachable, so a future change that re-opens
// either half fails here: (1) the collect-time source check does not constrain
// the target, and (2) the fence is what stops it.
func TestRunRecipe_CollectorOwnedTarget_ReachesWriteResultWithoutTheFence(t *testing.T) {
	// (1) A recipe whose SOURCE type matches the collect type exactly — so the
	// only pre-existing validation in RunRecipe is fully SATISFIED — still
	// declares a code target. Source validation is no protection at all.
	caller := targetFenceCaller(string(kgtypes.GraphCode))
	sink := &captureSink{}
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}

	_, err := RunRecipe(context.Background(), caller, sink, "src-graph", kgtypes.GraphWebRaw, opts)

	// (2) The fence — and nothing else on the path — is what refuses it.
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "source_graph_type",
		"the refusal is the TARGET fence, not the pre-existing source-type check")
	assert.Zero(t, sink.calls)
}
