// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// segment_branch_seed_rebuild_test.go — the OSS rail's end of the branch seed:
// a seeded branch ANSWERS FROM BASE'S CONTENT, and its rebuild axis is asked for
// nothing that has not changed since the watermark it inherited.
//
// THE ASSERTIONS ARE BEHAVIORAL, NOT CONFIGURATIONAL. Neither subtest looks at
// whether the seed was configured or whether the cache directory is non-empty:
// one runs a real search against the branch and requires a document only BASE
// ever held, and the other requires the segment_rebuild axis to have been asked
// with base's inherited watermark and served nothing.

// stampedNode is one node of the server-side corpus the fake serves, carrying the
// nanos its vector was stamped at. The stamp is the whole mechanism under test, so
// the fake filters on it exactly as the server's strict after-comparison does.
type stampedNode struct {
	id        string
	stampedAt int64
	vector    []byte
}

// scanRecord is one PipelineScan the fake received, reduced to the fields that say
// what the axis was asked FOR. The recorded set is asserted by EQUALITY rather than
// by a count: "the axis streamed nothing" and "the axis was never reached" produce
// the same count, and only the request itself distinguishes them.
type scanRecord struct {
	Axis                string
	GraphType           string
	GraphName           string
	AfterID             string
	AfterStampedAtNanos int64
	// ScanFromStampedAtNanos is the bound the scan itself reads from, recorded in the
	// same append as the retention floor beside it so a record's two values always
	// describe the same request.
	ScanFromStampedAtNanos int64
}

// seedScanEngine is the recording EngineService for this file. It differs from
// reconcileEngine in the one way this file needs: it SERVES BY WATERMARK, dropping
// every corpus node at or below the requested afterStampedAtNanos, so a scan that
// asked for the delta above a watermark and a scan that asked for everything are
// distinguishable in the response rather than only in the request.
//
// The UNEXPECTED arm is a catch-all recorder. A fake that silently returns an empty
// page for an axis the test never programmed makes a genuine zero and a scan of the
// wrong axis the same observation; every assertion below would then hold for a
// caller reading something else entirely.
type seedScanEngine struct {
	*countingEngine

	mu            sync.Mutex
	corpus        []stampedNode
	servedHorizon int64
	requests      []scanRecord
	unexpected    []string
}

func (e *seedScanEngine) PipelineScan(
	_ context.Context, req *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	m := req.Msg
	e.mu.Lock()
	e.requests = append(e.requests, scanRecord{
		Axis:                   m.GetAxis(),
		GraphType:              m.GetGraphType(),
		GraphName:              m.GetGraphName(),
		AfterID:                m.GetAfterId(),
		AfterStampedAtNanos:    m.GetAfterStampedAtNanos(),
		ScanFromStampedAtNanos: m.GetScanFromStampedAtNanos(),
	})
	if m.GetAxis() != "segment_rebuild" {
		e.unexpected = append(e.unexpected,
			fmt.Sprintf("UNEXPECTED axis %q on %s/%s", m.GetAxis(), m.GetGraphType(), m.GetGraphName()))
		e.mu.Unlock()
		return connect.NewResponse(&knowledgev1.PipelineScanResponse{}), nil
	}
	corpus, horizon := e.corpus, e.servedHorizon
	e.mu.Unlock()

	// Rows on the FIRST page only; the id-cursor scan terminates on an empty page.
	//
	// THE BOUND IS RESOLVED THE WAY THE HANDLER RESOLVES IT — the scan field when the
	// request carries one, the retention field otherwise. Mirroring the handler is the
	// whole point: a fake that filtered on the retention field alone would assert a
	// contract the server does not implement, and would serve a second pass the rows
	// the first already merged no matter what the client sent.
	bound := m.GetScanFromStampedAtNanos()
	if bound <= 0 {
		bound = m.GetAfterStampedAtNanos()
	}
	var items []*knowledgev1.PipelineScanItem
	if m.GetAfterId() == "" {
		for _, n := range corpus {
			if n.stampedAt <= bound {
				continue // strictly AFTER the RESOLVED bound, as the server compares
			}
			items = append(items, &knowledgev1.PipelineScanItem{
				NodeId:       n.id,
				BinaryVector: n.vector,
				Bm25Fields:   &knowledgev1.Bm25Fields{SymbolName: n.id},
			})
		}
	}
	return connect.NewResponse(&knowledgev1.PipelineScanResponse{
		Items:              items,
		ServedHorizonNanos: horizon,
	}), nil
}

func (e *seedScanEngine) recorded() ([]scanRecord, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]scanRecord(nil), e.requests...), append([]string(nil), e.unexpected...)
}

// buildSeedRebuildClient wires the OSS rail: a LOGGED-OUT router, which is what
// makes the Manager select the OSS-local L2-only segment source. On that rail the
// segment source IS the cache directory, so base's bucket is warmed by ordinary
// local writes and the branch seed copies those blobs — no injected source needed.
func buildSeedRebuildClient(t *testing.T, repos ...string) (*client, *seedScanEngine) {
	t.Helper()
	return buildSeedRebuildClientAt(t, t.TempDir(), repos...)
}

func buildSeedRebuildClientAt(t *testing.T, dir string, repos ...string) (*client, *seedScanEngine) {
	t.Helper()
	eng := &seedScanEngine{countingEngine: &countingEngine{}}

	mux := http.NewServeMux()
	engPath, engHdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(engPath, engHdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	local := graphclient.NewGraphClientForURL(srv.URL)
	t.Cleanup(local.CloseIdleConnections)
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute) // logged OUT → OSS-local source
	router := graphclient.NewRouter(local, srv.URL, staticTokenSource{tok: "tok"}, authState)

	c := &client{
		local:         local,
		router:        router,
		authState:     authState,
		segmentMgr:    segmentdist.NewManager(dir, 0),
		workingSet:    fixtureWorkingSet(repos...),
		localPresence: fixturePresence(),
	}
	// Every graph this manager touches lazily builds an HNSW and a BM25 engine, and
	// each engine starts a merger goroutine that only Manager.Close stops —
	// segmentdist's own TestManagerCloseStopsEveryEngineMerger pins exactly that.
	// Production never closes the client's manager because the daemon's exit ends
	// the process, but a test binary runs hundreds of these in one process.
	t.Cleanup(c.segmentMgr.Close)
	return c, eng
}

// seedFixtureVector builds a distinct vector of the length the engines enforce.
func seedFixtureVector(mark byte) []byte {
	v := make([]byte, 32)
	for i := range v {
		v[i] = mark
	}
	return v
}

// seedFixtureCorpusN is the size of base's fixture corpus, and it is sized ABOVE
// residentBackstopFloor (64) DELIBERATELY.
//
// Below that floor the delta re-emit declines as NOT APPLICABLE and the rebuild
// driver falls back to a full re-scan from zero — correctly, since a handful of
// resident documents is not a corpus to re-emit against. A one-document fixture
// therefore exercises the fallback rather than the delta path, and the changed-node
// control would read the whole corpus streaming back as if the seed had failed. The
// property this file is about — a rebuild costing work proportional to the CHANGE —
// only exists above the floor, so the fixture has to be a corpus rather than a
// token.
const seedFixtureCorpusN = 70

// warmBaseBucket writes BASE's corpus into its local buckets in BOTH formats and
// re-emits them, which is what puts real blobs in the L2 cache for the seed to copy.
// Exactly ONE document carries contentTerm: it is the provenance marker, and making
// it rare is also what gives the BM25 query something to discriminate on.
func warmBaseBucket(t *testing.T, ctx context.Context, c *client, repo, docID, contentTerm string) {
	t.Helper()
	vecDocs := make([]searchengine.Document, 0, seedFixtureCorpusN)
	fieldDocs := make([]searchengine.Document, 0, seedFixtureCorpusN)
	for i := range seedFixtureCorpusN {
		id := docID
		fields := map[string]string{
			searchengine.FieldSymbolName: docID,
			searchengine.FieldContent:    contentTerm,
		}
		if i > 0 {
			id = fmt.Sprintf("pkg/filler%d.go:Filler%d", i, i)
			fields = map[string]string{
				searchengine.FieldSymbolName: id,
				searchengine.FieldContent:    "ordinary corpus filler body",
			}
		}
		vecDocs = append(vecDocs, searchengine.Document{
			ID: searchengine.ExternalID(id), Vector: seedFixtureVector(byte('a' + i%26)),
		})
		fieldDocs = append(fieldDocs, searchengine.Document{
			ID: searchengine.ExternalID(id), Fields: fields,
		})
	}
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, vecDocs))
	require.NoError(t, c.segmentMgr.AddAndMarkDirtyFields(ctx, kgtypes.GraphCode, repo, fieldDocs))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
}

// unchangedServerCorpus mirrors, on the SERVER side, the corpus warmBaseBucket
// wrote locally — every node stamped BEFORE the watermark the branch inherits.
//
// IT MUST BE THE WHOLE CORPUS, not a token row. The changed-node control asserts
// the axis streams exactly ONE node, and that number only discriminates if a scan
// that fell back to reading from zero would return a visibly different one. With a
// single row on the server, "the delta streamed one" and "the full corpus streamed
// one" are the same observation.
func unchangedServerCorpus(docID string, watermark int64) []stampedNode {
	out := make([]stampedNode, 0, seedFixtureCorpusN)
	for i := range seedFixtureCorpusN {
		id := docID
		if i > 0 {
			id = fmt.Sprintf("pkg/filler%d.go:Filler%d", i, i)
		}
		out = append(out, stampedNode{
			id: id, stampedAt: watermark - 1_000, vector: seedFixtureVector(byte('a' + i%26)),
		})
	}
	return out
}

func seedHitIDs(hits []searchengine.Hit) []searchengine.ExternalID {
	out := make([]searchengine.ExternalID, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ID)
	}
	return out
}

// TestBranchSeed_NoSegmentRebuildStreamForUnchangedContent is the step's headline
// made observable, scoped to the OSS rail.
//
// BOTH SUBTESTS ARE REQUIRED AND THEY CATCH DIFFERENT THINGS. unchanged_streams_nothing
// catches an omitted seed: without the copied record the branch's watermark is zero
// and the axis is asked for the whole corpus. changed_node_control_streams catches a
// scanner that was never wired at all — which produces the same zero as a correct
// seed, and would make the first subtest pass for entirely the wrong reason.
func TestBranchSeed_NoSegmentRebuildStreamForUnchangedContent(t *testing.T) {
	ctx := opCtx()
	const repo = "seedRebuildRepo"
	const branch = repo + "@feature"
	const baseWatermark = int64(1_700_000_000_000_000_000)
	const baseDocID = "pkg/base.go:BaseDoc"
	const baseTerm = "quokkasignal"

	t.Run("unchanged_streams_nothing", func(t *testing.T) {
		c, eng := buildSeedRebuildClient(t, repo)
		// UNCHANGED: every node stamped BEFORE the watermark the branch inherits.
		eng.corpus = unchangedServerCorpus(baseDocID, baseWatermark)
		eng.servedHorizon = baseWatermark + 5_000

		warmBaseBucket(t, ctx, c, repo, baseDocID, baseTerm)
		// Base has a LANDED rebuild behind it. Without this the copied watermark is
		// zero, the branch's first scan is full-corpus, and the assertion below would
		// fail against a correct implementation.
		require.NoError(t, c.segmentMgr.SaveRebuildState(kgtypes.GraphCode, repo, baseWatermark, nil))

		// (a) THE BRANCH ANSWERS FROM BASE'S CONTENT. Touching the branch constructs
		// its engines, which is what runs the seed; the search then reads what the
		// seed copied.
		hits, err := c.segmentMgr.Search(ctx, kgtypes.GraphCode, branch, baseTerm, nil, 10)
		require.NoError(t, err)
		require.Contains(t, seedHitIDs(hits), searchengine.ExternalID(baseDocID),
			"the seeded branch must answer a query for a term only BASE ever wrote — that hit IS the content provenance")
		// AND THE HIT IS A MATCH, NOT A PASSTHROUGH: a term base never wrote returns
		// nothing, so the hit above cannot be an engine handing back its whole corpus.
		absent, err := c.segmentMgr.Search(ctx, kgtypes.GraphCode, branch, "wombatsignal", nil, 10)
		require.NoError(t, err)
		require.NotContains(t, seedHitIDs(absent), searchengine.ExternalID(baseDocID),
			"control: a term BASE never wrote must not hit, or the hit above says nothing about content")

		// FIXTURE PRECONDITION: the branch really did inherit base's watermark. Stated
		// separately so a seed that failed reads as a fixture failure rather than as
		// the behavioral claim below being false.
		gotW, _, err := c.segmentMgr.LoadRebuildState(kgtypes.GraphCode, branch)
		require.NoError(t, err)
		require.Equal(t, baseWatermark, gotW, "fixture precondition: the branch inherited base's watermark")

		// (b) THE AXIS IS ASKED FOR NOTHING. One page, carrying base's watermark, served
		// zero rows.
		out, err := tools.RebuildSegments(ctx, c.PipelineScanner(), c.SegmentShipper(), kgtypes.GraphCode, branch, false)
		require.NoError(t, err)
		require.True(t, out.Ran, "a real run happened — this is a zero, not a coalesce")
		require.Zero(t, out.Scanned, "the rebuild axis streamed NOTHING for content the branch already holds from base")

		got, unexpected := eng.recorded()
		require.Empty(t, unexpected, "the fake received an axis this test never programmed")
		require.Equal(t, []scanRecord{{
			Axis: "segment_rebuild", GraphType: string(kgtypes.GraphCode), GraphName: branch,
			AfterID: "", AfterStampedAtNanos: baseWatermark,
		}}, got, "exactly one segment_rebuild page, scoped to the INHERITED watermark")
	})

	t.Run("changed_node_control_streams", func(t *testing.T) {
		const changedID = "pkg/changed.go:Changed"
		c, eng := buildSeedRebuildClient(t, repo)
		// The same unchanged corpus, plus ONE node stamped AFTER the inherited
		// watermark by construction rather than by timing luck. A scan that fell back
		// to reading from zero would stream all 71; a watermark-scoped one streams 1.
		eng.corpus = append(unchangedServerCorpus(baseDocID, baseWatermark),
			stampedNode{id: changedID, stampedAt: baseWatermark + 1_000, vector: seedFixtureVector('c')})
		eng.servedHorizon = baseWatermark + 5_000

		warmBaseBucket(t, ctx, c, repo, baseDocID, baseTerm)
		require.NoError(t, c.segmentMgr.SaveRebuildState(kgtypes.GraphCode, repo, baseWatermark, nil))

		_, err := c.segmentMgr.Search(ctx, kgtypes.GraphCode, branch, baseTerm, nil, 10)
		require.NoError(t, err)
		gotW, _, err := c.segmentMgr.LoadRebuildState(kgtypes.GraphCode, branch)
		require.NoError(t, err)
		require.Equal(t, baseWatermark, gotW, "fixture precondition: the branch inherited base's watermark")

		out, err := tools.RebuildSegments(ctx, c.PipelineScanner(), c.SegmentShipper(), kgtypes.GraphCode, branch, false)
		require.NoError(t, err)
		require.Equal(t, 1, out.Scanned,
			"THE KNOWN-POSITIVE CONTROL: the same wiring that streamed zero above streams the changed node here, "+
				"so the zero is a scoped scan rather than a scanner nothing ever reached")

		got, unexpected := eng.recorded()
		require.Empty(t, unexpected, "the fake received an axis this test never programmed")
		require.Equal(t, []scanRecord{
			{Axis: "segment_rebuild", GraphType: string(kgtypes.GraphCode), GraphName: branch,
				AfterID: "", AfterStampedAtNanos: baseWatermark},
			// The cursor advances past the served row and the next page is empty, which
			// is what ends the scan.
			{Axis: "segment_rebuild", GraphType: string(kgtypes.GraphCode), GraphName: branch,
				AfterID: changedID, AfterStampedAtNanos: baseWatermark},
		}, got, "the axis was asked for exactly the changed node, still scoped to the inherited watermark")
	})
}
