// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// segment_reconcile_clientkit_test.go holds the reconcile fixtures' CLIENT BUILDERS.
//
// THE SEGMENT BACKEND IS GONE FROM THE KIT, and that is most of what changed. Every
// builder used to thread a *fakeSegBackend — a fake cloud-agent control plane — and
// hand it to the segment manager's transport option so the consumer Manager would run
// on the CLOUD source. There is no transport option and no cloud source: a Manager is
// L2-local, always. The four wrappers that existed only to expose that backend
// collapse into two, separated by whether the caller needs the cache DIR (it does
// when it wants to warm L2 through a separate producer Manager rooted at the same
// place — the daemon-restart shape).
//
// THE AUTH WIRING STAYS, and its reason changed. It used to be there to keep the
// Manager on the GCS source by looking logged-in; it is there now only because
// graphclient.NewRouter needs a token source and an auth state to construct. It no
// longer selects anything about segments.

// seedWorkingSet builds the working set a fixture DECLARES it has interacted with.
// Every background arm reads that set, so a fixture that declares nothing is a
// client that has interacted with nothing and correctly does no work at all —
// which is why the declaration belongs in the constructor rather than in whichever
// tests happen to notice they need it.
func seedWorkingSet(refs ...workingset.Ref) *workingset.Set {
	s := workingset.New()
	for _, r := range refs {
		s.Admit(r.GraphType, r.Name, "fixture")
	}
	return s
}

// fixtureWorkingSet is the reconcile fixtures' standing declaration: the code repos
// the caller named, plus knowledge/default. The knowledge member is not a seed in
// the production sense — it is this FIXTURE asserting that its client interacted
// with the knowledge graph, which is what every one of these tests has always
// assumed by walking it.
func fixtureWorkingSet(codeRepos ...string) *workingset.Set {
	refs := []workingset.Ref{{GraphType: kgtypes.GraphKnowledge, Name: "default"}}
	for _, r := range codeRepos {
		refs = append(refs, workingset.Ref{GraphType: kgtypes.GraphCode, Name: r})
	}
	return seedWorkingSet(refs...)
}

// fixturePresence is the companion declaration to fixtureWorkingSet: the code
// repos these fixtures name are treated as CHECKED OUT on this machine. Their
// subject is what the reconcile does with a member, not whether the member's
// repo happens to exist under a temp dir, and the real predicate consults a
// machine-local manifest that a test machine has no reason to carry. The
// presence condition itself is the subject of
// TestSegmentBearingGraphs_SkipsAbsentCodeRepo, which states its own.
func fixturePresence() func(kgtypes.GraphType, string) bool {
	return func(kgtypes.GraphType, string) bool { return true }
}

// fastloadVecDocs builds n searchengine.Documents with deterministic 32-byte
// vectors, prefixed so separate batches seal distinct segments.
//
// RELOCATED from the deleted segment_fastload_heal_test.go. It is a pure fixture
// generator with no dependence on the rail, and several surviving reconcile tests
// consume it, so it moved rather than dying with the file that happened to declare
// it.
func fastloadVecDocs(prefix string, n int) []searchengine.Document {
	docs := make([]searchengine.Document, 0, n)
	for i := range n {
		vec := make([]byte, 32)
		for b := range vec {
			vec[b] = byte((i + b) % 251)
		}
		docs = append(docs, searchengine.Document{
			ID:     prefix + "-" + string(rune('a'+i%26)) + "-" + itoaFixture(i),
			Vector: vec,
			Fields: map[string]string{"body": prefix + " body " + itoaFixture(i)},
		})
	}
	return docs
}

// itoaFixture keeps fastloadVecDocs free of an strconv import in a file that needs
// none otherwise.
func itoaFixture(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// seedL2Corpus writes a real, decodable corpus into the L2 cache a reconcile client
// reads from, by running a SEPARATE producer Manager rooted at the same dir.
//
// IT IS THE SUCCESSOR TO shipHNSW / shipHNSWFor, and it differs in a way callers must
// understand rather than paper over. Those seeded a MANIFEST of digests carrying
// per-segment doc_counts, because the probe under test read its denominator off that
// manifest. There is no manifest, and an L2 read carries no per-segment doc count at
// all — segment identity is a content hash and nothing records a count beside it — so
// a doc-count knob has nothing to set. What a test can still
// arrange is whether the graph HAS a corpus and how big it is, which is what the
// surviving degeneracy decision actually consumes: a resident count against the
// server's embedded count.
//
// A TEST THAT WANTED "a manifest with no backing objects" — the post-restart empty
// load — arranges it by seeding NOTHING here and setting embedded>0 on the client.
func seedL2Corpus(t *testing.T, dir string, gt kgtypes.GraphType, name string, n int) {
	t.Helper()
	// segmentCacheDirFor, NOT dir — dir is the GRAPH-STORAGE root, and production
	// roots the manager one level down at <root>/segments. Constructing at dir here
	// would put the producer's blobs somewhere no consumer built the production way
	// ever looks, and nothing would error: the reads would just come back empty.
	producer := segmentdist.NewManager(segmentCacheDirFor(dir), 0)
	t.Cleanup(producer.Close)
	ctx := context.Background()
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, fastloadVecDocs(name, n)))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, gt, name))
}

// buildReconcileClient wires a *client over an EngineService h2c server. The
// returned engine handle lets a test seed scan pages + read the per-graph
// PipelineScan count.
func buildReconcileClient(t *testing.T, codeRepos ...string) (*client, *reconcileEngine) {
	t.Helper()
	return buildReconcileClientWith(t, 0, codeRepos...)
}

// buildReconcileClientWith exposes the embedded knob (BinaryVectorCount Stats
// serves) so the heal path's degeneracy decision can be ARMED (nonzero). A test
// seeds a real decodable corpus by writing through a producer Manager rooted at the
// same cache dir — see buildReconcileClientWithDir.
func buildReconcileClientWith(
	t *testing.T, embedded int32, codeRepos ...string,
) (*client, *reconcileEngine) {
	t.Helper()
	c, eng, _ := buildReconcileClientWithDir(t, embedded, codeRepos...)
	return c, eng
}

// buildReconcileClientWithDir also returns the client's L2 cache base dir, so a test
// can warm the on-disk cache through a SEPARATE producer Manager rooted at the SAME
// dir — the daemon-restart shape: a prior run warmed the disk, then a fresh consumer
// Manager imports from it.
func buildReconcileClientWithDir(
	t *testing.T, embedded int32, codeRepos ...string,
) (*client, *reconcileEngine, string) {
	t.Helper()
	return buildReconcileClientOnDir(t, embedded, t.TempDir(), codeRepos...)
}

// buildReconcileClientOnDir is buildReconcileClientWithDir over a SUPPLIED cache dir,
// so a caller can point a fresh client at a directory that already holds a corpus
// rather than paying to build one.
//
// POPULATE THE DIRECTORY BEFORE CALLING THIS, NEVER AFTER. The disk cache INDEXES ITS
// ROOT AT CONSTRUCTION, so any .seg file copied in after the Manager exists is
// invisible to it: Keys() will not list it, a load will not import it, and the test
// will read an empty pool while a full directory sits underneath. Nothing errors —
// the failure is a silent empty corpus, which reads exactly like the collapse several
// tests in this package are trying to detect. Seed first, then construct.
//
// IT REPLACES buildReconcileClientOnBackend. That took a pre-loaded *fakeSegBackend —
// the corpus lived in the fake GCS control plane and the client read it over the
// wire. The corpus now lives in the L2 cache, so the thing worth handing a fresh
// client is the DIRECTORY.
func buildReconcileClientOnDir(
	t *testing.T, embedded int32, dir string, codeRepos ...string,
) (*client, *reconcileEngine, string) {
	t.Helper()
	eng := &reconcileEngine{
		countingEngine: &countingEngine{},
		namesByType:    map[string][]string{string(kgtypes.GraphCode): codeRepos},
		embedded:       embedded,
		scanItems:      map[string][]*knowledgev1.PipelineScanItem{},
		scanCalls:      map[string]int{},
		deltaScanCalls: map[string]int{},
	}

	mux := http.NewServeMux()
	engPath, engHdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(engPath, engHdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	local := graphclient.NewGraphClientForURL(srv.URL)
	t.Cleanup(local.Close)

	// The auth state exists because NewRouter needs one, not because it selects a
	// segment source any more — there is only one source.
	store := newFakeAuthStore()
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-stub"))
	authState := auth.NewAuthState(store, time.Minute)
	router := graphclient.NewRouter(local, srv.URL, staticTokenSource{tok: "tok"}, authState)

	c := &client{
		local:     local,
		router:    router,
		authState: authState,
		// segmentCacheDirFor(dir), matching ensureSegmentManager: the client is handed
		// a GRAPH-STORAGE root and roots its segment cache at <root>/segments. A fixture
		// that constructed at dir itself would test the code against a layout production
		// never produces, and l2SegmentIDs — which derives its walk root the production
		// way — would read an empty tree while a full one sat one level up.
		segmentMgr: segmentdist.NewManager(segmentCacheDirFor(dir), 0),
		workingSet: fixtureWorkingSet(codeRepos...),

		localPresence: fixturePresence(),
	}
	// Only Manager.Close stops the per-engine merger goroutines this spawns. This
	// helper feeds most of the reconcile tests, so one missed teardown here is one
	// leaked merger pair per test in the package.
	t.Cleanup(c.segmentMgr.Close)
	return c, eng, dir
}

// armIsDegenerate answers what the deleted one-call degeneracy probe used to answer,
// recomposed from the two pieces that call split into.
//
// THE SPLIT IS THE POINT. That call both OBSERVED the arm and DECIDED whether it was
// degenerate, and it decided against the SHIPPED doc count. The observation half
// survives as ResidentObservationsByFormat and the decision half moved to the caller
// holding the EMBEDDED count — so a test that wants the old one-call answer has to
// supply the denominator itself, which is exactly the honesty the split bought: the
// denominator is now visible at every call site instead of being read off a manifest.
//
//nolint:unparam // gt is the intentional named API: it selects the graph the observation is read for, and these fixtures happen to exercise code graphs
func armIsDegenerate(
	t *testing.T, mgr *segmentdist.Manager, gt kgtypes.GraphType, name string, embedded int,
) bool {
	t.Helper()
	obs, err := mgr.ResidentObservationsByFormat(context.Background(), gt, name)
	require.NoError(t, err)
	for _, o := range obs {
		if o.Format == hnsw.New().Name() {
			require.NoError(t, o.Err, "the HNSW arm must be measurable")
			// hnswPoolLost, NOT the retired ratio band: this helper must ask the SAME
			// question the reconcile sweep now asks, or a PRE-state assertion built on it
			// would claim a graph is armed that the sweep will decline to rebuild.
			return hnswPoolLost(o.ResidentAfterLoad, embedded)
		}
	}
	require.FailNow(t, "no HNSW observation returned")
	return false
}

// l2SegmentIDs returns the content-hash segment ids one graph+format holds on disk.
//
// IT IS THE SUCCESSOR to reading a published manifest's digests, and it preserves the
// property that made that reading worth taking: the ids are CONTENT HASHES of durable
// bytes, so a re-emit changes the set while a change that only cleared an in-memory
// live bit leaves it byte-identical. That discrimination is the whole point — it is
// what separates "the delete reached the pool" from "the delete never left this
// process's memory".
//
// It walks the cache root rather than asking the Manager, because the Manager exposes
// a COUNT and this needs the identities: a re-emit that replaced one blob with another
// moves no count at all.
//
// IT SCOPES BY GRAPH NAME AND FORMAT, NOT BY GRAPH TYPE. The walk matches the cache
// path against name and format only, so a graph of a different TYPE sharing this name
// would be counted too. It used to take a graph type as well, which every caller passed
// as GraphCode and the body never read — a parameter promising a narrowing it did not
// perform. Callers here are all code graphs, so name+format is unambiguous; a caller
// that ever needs the type narrowing must add it to the filter, not just to the
// signature.
func l2SegmentIDs(t *testing.T, dir string, name, format string) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	root := segmentCacheDirFor(dir)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".seg" {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(rel, name) && strings.Contains(rel, format) {
			out[strings.TrimSuffix(filepath.Base(path), ".seg")] = struct{}{}
		}
		return nil
	})
	// A MISSING ROOT IS A REAL STATE, EVERY OTHER ERROR IS A BROKEN FIXTURE. Callers
	// read this set BEFORE the first publish, where the cache root does not exist yet
	// and the empty set is the truthful answer. Any other walk failure used to be
	// swallowed into that same empty set — and since callers compare this set against
	// another one, a short set makes a set-equality assertion pass while measuring
	// nothing. It fails loudly instead.
	if !errors.Is(walkErr, fs.ErrNotExist) {
		require.NoError(t, walkErr,
			"walking the segment cache root must succeed; a swallowed walk error yields a short set and makes the callers' set comparisons vacuous")
	}
	return out
}
