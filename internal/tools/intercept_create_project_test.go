// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// fakeBackend is a scripted backends.Backend implementation used by
// intercept tests. Records calls; returns scripted refs and errors.
type fakeBackend struct {
	name string

	groupsResult []backends.Group
	groupsErr    error

	createProjectRef backends.RemoteRef
	createProjectErr error
	createProjectArg backends.ProjectCreateArgs

	createTicketRef backends.RemoteRef
	createTicketErr error
	createTicketArg backends.TicketCreateArgs

	updateProjectErr  error
	updateTicketErr   error
	archiveProjectErr error
	archiveTicketErr  error

	updateProjectCalls  int
	updateTicketCalls   int
	archiveProjectCalls int
	archiveTicketCalls  int
}

func (f *fakeBackend) Name() string {
	if f.name == "" {
		return "linear"
	}
	return f.name
}
func (f *fakeBackend) Groups(_ context.Context) ([]backends.Group, error) {
	return f.groupsResult, f.groupsErr
}
func (f *fakeBackend) SyncGroup(_ context.Context, _ string) (backends.Snapshot, error) {
	return backends.Snapshot{}, nil
}
func (f *fakeBackend) CreateProject(_ context.Context, a backends.ProjectCreateArgs) (backends.RemoteRef, error) {
	f.createProjectArg = a
	return f.createProjectRef, f.createProjectErr
}
func (f *fakeBackend) UpdateProject(_ context.Context, _ backends.RemoteRef, _ backends.ProjectDiff) error {
	f.updateProjectCalls++
	return f.updateProjectErr
}
func (f *fakeBackend) ArchiveProject(_ context.Context, _ backends.RemoteRef) error {
	f.archiveProjectCalls++
	return f.archiveProjectErr
}
func (f *fakeBackend) CreateTicket(_ context.Context, a backends.TicketCreateArgs) (backends.RemoteRef, error) {
	f.createTicketArg = a
	return f.createTicketRef, f.createTicketErr
}
func (f *fakeBackend) UpdateTicket(_ context.Context, _ backends.RemoteRef, _ backends.TicketDiff) error {
	f.updateTicketCalls++
	return f.updateTicketErr
}
func (f *fakeBackend) ArchiveTicket(_ context.Context, _ backends.RemoteRef) error {
	f.archiveTicketCalls++
	return f.archiveTicketErr
}

// fakeResolver wires a single backend; nil means no backend.
type fakeResolver struct {
	def    backends.Backend
	byName map[string]backends.Backend
}

func (f fakeResolver) Default() backends.Backend { return f.def }
func (f fakeResolver) ByName(name string) backends.Backend {
	if b, ok := f.byName[name]; ok {
		return b
	}
	return nil
}

// interceptTestDeps satisfies ClientDeps for intercept tests. Wires
// only the BackendResolver + GraphCaller (the only two the intercepts
// touch). Everything else is nil.
type interceptTestDeps struct {
	backend backends.Backend
	byName  map[string]backends.Backend
	gc      GraphCaller
	crud    GraphTypeCRUDAPI
	forcer  ReflectionForcer
	// blindSpots is the faceted report the fake BlindSpotProvider returns. The zero
	// value (Computed=false) is the cold sentinel, so a test exercising the
	// cold-start path leaves it unset. blindSpotProviderNil flips BlindSpotProvider()
	// to return a nil interface so a test can exercise the loop-not-running path.
	blindSpots           clientthought.BlindSpotReport
	blindSpotProviderNil bool
	// clusters/clusterProfile/clusterComputed back the fake ClusterProvider; tensions/
	// tensionsComputed back the fake TensionsProvider. clusterProviderNil/
	// tensionsProviderNil flip the respective accessor to return a nil interface so a
	// test can exercise the loop-not-running path. Zero values (computed=false) are the
	// cold sentinel for the cold-start tests.
	// corpusThoughts/corpusCharges are threaded into the fake ClusterProvider's
	// resident-snapshot projections. Empty (the zero value) is cold, which is what
	// keeps every pre-existing test on the wire path it already exercises.
	clusters            []clientthought.ThoughtCluster
	clusterProfile      *clientthought.PersonalityProfile
	corpusThoughts      []*knowledgev1.Node
	corpusCharges       []*knowledgev1.Node
	tensions            []clientthought.TensionReport
	clusterComputed     bool
	tensionsComputed    bool
	clusterProviderNil  bool
	tensionsProviderNil bool
	// propNotReady flips PropReady() to false so a test can exercise the
	// bind-first wiring-window gate (bind-first startup) on the propagate handler. Zero value
	// keeps the reflection loop ready, so every pre-existing test exercises the
	// wired path.
	propNotReady bool
	// deleter backs SegmentDeleter(). The zero value is nil — the headless client
	// with no segment engine — so every fake that does not set it keeps the
	// behavior it had before this field existed.
	deleter SegmentDeleter

	// shipper backs SegmentShipper(). The zero value is nil — the headless client
	// again — so every other fake ClientDeps in this package is unaffected. Without
	// it the record-seeding arms are unreachable and their tests record zero saves.
	shipper SegmentShipper

	// searcher backs SegmentManager(). The zero value is nil — the headless client
	// with no segment engine, which is what every pre-existing fake in this package
	// relies on. The Phase-5 query parity harness sets it: the six ranked-search
	// arms bail with the client-segment-engine-unavailable error before issuing a
	// read when it is nil, so their cells would measure a degrade path rather than
	// the arm. TestQueryArmParity_DeclaredClassMatchesObservedBehavior goes red on
	// every search-arm row if this knob is dropped.
	searcher SegmentSearcher
}

// fakeBlindSpotProvider serves a constructed faceted report for the cache-serve
// handler tests — a pure value return, no graph reads.
type fakeBlindSpotProvider struct {
	report clientthought.BlindSpotReport
}

func (f fakeBlindSpotProvider) GetBlindSpots() clientthought.BlindSpotReport { return f.report }

// fakeClusterProvider serves constructed clusters + a personality profile for the
// cache-serve personality/summary handler tests — a pure value return, no graph reads.
// It ALSO satisfies clientthought.CorpusSource + ChargeCorpusSource, which is what
// corpusSourceFromDeps type-asserts for. Both projections report COLD on the zero
// value, so every pre-existing test is byte-identical: the source stops being nil,
// but a cold source makes every consumer take exactly the wire path it takes today.
type fakeClusterProvider struct {
	clusters []clientthought.ThoughtCluster
	profile  *clientthought.PersonalityProfile
	computed bool
	// corpusThoughts/corpusCharges back the resident-snapshot projections. Empty
	// means cold, which is the zero value.
	corpusThoughts []*knowledgev1.Node
	corpusCharges  []*knowledgev1.Node
}

func (f fakeClusterProvider) GetClustersCached() ([]clientthought.ThoughtCluster, *clientthought.PersonalityProfile, bool) {
	return f.clusters, f.profile, f.computed
}

func (f fakeClusterProvider) CorpusSnapshot() ([]*knowledgev1.Node, bool) {
	return f.corpusThoughts, len(f.corpusThoughts) > 0
}

func (f fakeClusterProvider) ChargeSnapshot() ([]*knowledgev1.Node, bool) {
	return f.corpusCharges, len(f.corpusCharges) > 0
}

// fakeTensionsProvider serves constructed tension reports for the cache-serve
// tensions handler tests — a pure value return, no graph reads.
type fakeTensionsProvider struct {
	tensions []clientthought.TensionReport
	computed bool
}

func (f fakeTensionsProvider) GetTensions() ([]clientthought.TensionReport, bool) {
	return f.tensions, f.computed
}

func (d interceptTestDeps) LocalLiveness() LocalLiveness    { return nil }
func (d interceptTestDeps) Sink() collector.Sink            { return nil }
func (d interceptTestDeps) RootDir() string                 { return "" }
func (d interceptTestDeps) UsageAnalyzer() UsageAnalyzerAPI { return nil }

func (d interceptTestDeps) PropReady() bool     { return !d.propNotReady }
func (d interceptTestDeps) PipelineReady() bool { return true }

func (d interceptTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI { return d.crud }
func (d interceptTestDeps) Embedder() embed.BinaryEmbedder  { return nil }
func (d interceptTestDeps) BackendResolver() BackendResolver {
	return fakeResolver{def: d.backend, byName: d.byName}
}
func (d interceptTestDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d interceptTestDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d interceptTestDeps) SegmentManager() SegmentSearcher              { return d.searcher }
func (d interceptTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d interceptTestDeps) SegmentShipper() SegmentShipper               { return d.shipper }
func (d interceptTestDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d interceptTestDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d interceptTestDeps) SegmentDeleter() SegmentDeleter           { return d.deleter }
func (d interceptTestDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d interceptTestDeps) PipelineScanner() PipelineScanner         { return nil }

func (d interceptTestDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d interceptTestDeps) ReflectionForcer() ReflectionForcer {
	if d.forcer == nil {
		return nil
	}
	return d.forcer
}

func (d interceptTestDeps) SimilarityForcer() SimilarityForcer { return nil }

func (d interceptTestDeps) BlindSpotProvider() BlindSpotProvider {
	if d.blindSpotProviderNil {
		return nil
	}
	return fakeBlindSpotProvider{report: d.blindSpots}
}

func (d interceptTestDeps) ClusterProvider() ClusterProvider {
	if d.clusterProviderNil {
		return nil
	}
	return fakeClusterProvider{
		clusters:       d.clusters,
		profile:        d.clusterProfile,
		computed:       d.clusterComputed,
		corpusThoughts: d.corpusThoughts,
		corpusCharges:  d.corpusCharges,
	}
}

func (d interceptTestDeps) TensionsProvider() TensionsProvider {
	if d.tensionsProviderNil {
		return nil
	}
	return fakeTensionsProvider{tensions: d.tensions, computed: d.tensionsComputed}
}

func TestInterceptCreateProject_NoBackend_ClaimsLocalOnly(t *testing.T) {
	// Phase 3a: no-backend path is now claimed client-side.
	// The server has no create_project handler, so this intercept
	// MUST claim the call to produce a real response.
	fc := &fakeGraphCaller{
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-x"]}`}},
		},
	}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s"}`),
	})
	assert.True(t, handled, "no-backend path is now claimed client-side")
	assert.False(t, res.IsError, "local-only create must succeed: %s", toolResultText(res))
}

func TestInterceptCreateProject_WrongTool_FallsThrough(t *testing.T) {
	deps := interceptTestDeps{backend: &fakeBackend{}, gc: &fakeGraphCaller{}}
	handled, _ := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{Name: "query"})
	assert.False(t, handled)
}

func TestInterceptCreateProject_LinearError_ReturnsErrorResult(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-1"}},
		createProjectErr: errors.New("linear: 401 unauthorized"),
	}
	deps := interceptTestDeps{backend: fb, gc: &fakeGraphCaller{}}
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s","group":"FUL"}`),
	})
	assert.True(t, handled)
	assert.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "401 unauthorized")
}

func TestInterceptCreateProject_Success_StampsBackendMetadata(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	// The client no longer forwards create_project — instead
	// it issues mutate(create_batch). The fake's mutateResult feeds the
	// create_batch RPC. Returned ids are read off the JSON.
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s","group":"FUL"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "success should not be an error result: %s", toolResultText(res))
	// One CREATE Mutation Execute (carrier path) with backend metadata stamped on
	// the project NodeBody (the create rides a MutationPlan now,
	// not the formatted create_batch wire envelope).
	require.Len(t, fc.execMutations, 1)
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	md := m.GetNodeBodies()[0].GetMetadata()
	assert.Equal(t, "linear", md["backend"])
	assert.Equal(t, "proj-uuid", md["linear_id"])
	assert.Equal(t, "https://example.invalid/p", md["external_url"])
	assert.Equal(t, "team-uuid", md["linear_group_id"])
	assert.Equal(t, "FUL", md["linear_group_key"])
}

func TestInterceptCreateProject_GroupNotFound_Errors(t *testing.T) {
	fb := &fakeBackend{
		groupsResult: []backends.Group{{Key: "FUL", ID: "team-1"}},
	}
	deps := interceptTestDeps{backend: fb, gc: &fakeGraphCaller{}}
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s","group":"WRONG"}`),
	})
	assert.True(t, handled)
	assert.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "WRONG")
	assert.Contains(t, toolResultText(res), "FUL")
}

func TestInterceptCreateProject_AutoDefaultsSingleGroup(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-x"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError)
	assert.Equal(t, "FUL", fb.createProjectArg.GroupKey, "single-group auto-default")
}

func TestInterceptCreateProject_ForwardError_NamesLinearID(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	// PersistBatch failure → surfaced as "local mirror failed" with
	// Linear identifiers so the operator can reconcile.
	fc := &fakeGraphCaller{mutateError: errors.New("connect: refused")}
	deps := interceptTestDeps{backend: fb, gc: fc}
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"s","group":"FUL"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError)
	body := toolResultText(res)
	assert.Contains(t, body, "Linear create succeeded")
	assert.Contains(t, body, "proj-uuid")
	assert.Contains(t, body, "https://example.invalid/p")
	assert.Contains(t, body, "local mirror failed")
}

// TestInterceptCreateProject_LocalOnly_SummaryClampsAndWarns asserts the
// no-backend (local-only) path clamps an over-cap author summary and surfaces a
// warning in the result, AND that the persisted project node carries the clamped
// summary. Fails-when-absent: an over-cap summary would error, the persisted
// node body Summary would exceed 500 runes, or the (newly added) warnings
// channel would drop the warning.
func TestInterceptCreateProject_LocalOnly_SummaryClampsAndWarns(t *testing.T) {
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{gc: fc}
	longSummary := strings.Repeat("a", 600)
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"` + longSummary + `","format":"json"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "over-cap summary must clamp + create on the local-only path: %s", toolResultText(res))
	msg := toolResultText(res)
	assert.Contains(t, msg, "summary")
	assert.Contains(t, msg, "clamped")
	// The persisted node body must carry the CLAMPED summary, not the original.
	require.Len(t, fc.execMutations, 1)
	require.Len(t, fc.execMutations[0].GetNodeBodies(), 1)
	stored := fc.execMutations[0].GetNodeBodies()[0].GetSummary()
	assert.LessOrEqual(t, utf8.RuneCountInString(stored), 500, "persisted summary must be clamped to <=500 runes")
}

// TestInterceptCreateProject_Backend_ClampsBeforeLinearPush is the MANDATORY
// reviewer-required test: it proves the clamp runs BEFORE buildAndPushProjectToLinear,
// so the Linear CreateProject receives the clamped (<=500 rune) summary — keeping
// the remote and local mirror consistent. It also asserts the persisted node body
// is clamped and the result carries the clamp warning. Fails-when-absent: if the
// clamp ran after (or only on) the local path, fb.createProjectArg.Summary would
// be the full 600-rune string and exceed 500.
func TestInterceptCreateProject_Backend_ClampsBeforeLinearPush(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	longSummary := strings.Repeat("a", 600)
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"p","description":"d","summary":"` + longSummary + `","group":"FUL","format":"json"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "over-cap summary must clamp + create on the backend path: %s", toolResultText(res))
	// Linear must receive the CLAMPED summary (clamp ran before the push).
	assert.LessOrEqual(t, utf8.RuneCountInString(fb.createProjectArg.Summary), 500,
		"Linear CreateProject must receive the clamped (<=500 rune) summary")
	assert.NotEmpty(t, fb.createProjectArg.Summary, "Linear summary should be the clamped value, not empty")
	// The persisted node body must also be clamped, and the result must warn.
	require.Len(t, fc.execMutations, 1)
	require.Len(t, fc.execMutations[0].GetNodeBodies(), 1)
	assert.LessOrEqual(t, utf8.RuneCountInString(fc.execMutations[0].GetNodeBodies()[0].GetSummary()), 500,
		"persisted summary must be clamped to <=500 runes")
	assert.Contains(t, toolResultText(res), "clamped")
}
