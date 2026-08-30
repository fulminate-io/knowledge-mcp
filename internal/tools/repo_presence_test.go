// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestLocalCodeRepoPresent_ManifestAndDir pins that presence needs BOTH halves.
// The background loops gate real work on this answer, so each way of being
// absent is covered separately: a name the manifest never recorded, and a name
// it did record whose checkout has since been deleted. The recorded-and-present
// case is the known-positive control — without it a predicate stuck at false
// would satisfy every other case here.
func TestLocalCodeRepoPresent_ManifestAndDir(t *testing.T) {
	withTestManifest(t)

	// A real directory, recorded under its basename exactly as a code collect
	// records it.
	presentDir := t.TempDir()
	recordCollectedRepo("code", presentDir)

	// A recorded repo whose checkout is then removed — the manifest still names
	// it, but this machine no longer holds it. The removal happens below, after
	// every record.
	deletedDir := filepath.Join(t.TempDir(), "deleted-repo")
	require.NoError(t, os.Mkdir(deletedDir, 0o750))
	recordCollectedRepo("code", deletedDir)

	// A recorded name pointing at a FILE rather than a directory: the path
	// exists, so an existence-only check would wrongly report present.
	fileDir := t.TempDir()
	filePath := filepath.Join(fileDir, "not-a-repo")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))
	recordCollectedRepo("code", filePath)

	// THE DELETION MUST BE THE LAST FIXTURE ACTION. A manifest write prunes rows
	// whose recorded directory has vanished, so deleting before a later record
	// would sweep this row out of the fixture — and the absence subtest below
	// would then pass because the NAME is gone, not because presence re-checked
	// the disk.
	require.NoError(t, os.RemoveAll(deletedDir))

	t.Run("recorded and present", func(t *testing.T) {
		assert.True(t, LocalCodeRepoPresent(filepath.Base(presentDir)),
			"control: a repo the manifest names whose checkout exists IS present")
	})
	t.Run("never recorded", func(t *testing.T) {
		assert.False(t, LocalCodeRepoPresent("a-repo-this-machine-never-collected"),
			"a name the manifest does not carry cannot be present")
	})
	t.Run("recorded but deleted", func(t *testing.T) {
		assert.False(t, LocalCodeRepoPresent("deleted-repo"),
			"a manifest entry outlives the checkout it points at; presence must re-check the disk")
	})
	t.Run("manifest still names the deleted repo", func(t *testing.T) {
		// THE CATCHER for the subtest above. That one asserts an ABSENCE, so it
		// stays green even when the row it depends on has been swept out of the
		// fixture. This one is POSITIVE: it goes red if a later manifest write
		// prunes the row, or if the prune ever reaches the read path.
		_, ok := lookupRepoDir("deleted-repo")
		assert.True(t, ok,
			"the manifest row must outlive the checkout it points at, or the absence assertion above is vacuous")
	})
	t.Run("recorded but not a directory", func(t *testing.T) {
		assert.False(t, LocalCodeRepoPresent("not-a-repo"),
			"a path that exists but is not a directory is not a checkout")
	})
	t.Run("empty name", func(t *testing.T) {
		assert.False(t, LocalCodeRepoPresent(""))
	})
}

// recordOrderStubType is the collector name the ordering test drives. It is
// literally "code" because recordCollectedRepo fires for that type ALONE, so no
// other name can exercise the ordering at all. Registering it is safe and is not
// a duplicate: package tools does not link the real code collector (codesync),
// which is also why a stub has to supply the sink write.
const recordOrderStubType = "code"

var recordOrderStubOnce sync.Once

// recordOrderStubCollector stands in for the code collector, doing the one thing
// this test needs a collector to do: drive the sink.
type recordOrderStubCollector struct{}

func (recordOrderStubCollector) Name() string { return recordOrderStubType }

func (recordOrderStubCollector) Collect(ctx context.Context, id string, opts collector.CollectOptions) (*collectorwire.CollectResult, error) {
	res := &collectorwire.CollectResult{GraphType: kgtypes.GraphCode, GraphName: filepath.Base(id)}
	if opts.Sink != nil {
		if err := opts.Sink.WriteResult(ctx, recordOrderStubType, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// orderAssertingSink answers the ordering question from INSIDE the collect: at
// the moment the collector ships its result, does this machine's manifest
// already resolve the repo?
type orderAssertingSink struct {
	repo string

	mu             sync.Mutex
	called         bool
	resolvedAtCall bool
}

func (s *orderAssertingSink) WriteResult(_ context.Context, _ string, _ *collectorwire.CollectResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.called = true
	s.resolvedAtCall = LocalCodeRepoPresent(s.repo)
	return nil
}

func (s *orderAssertingSink) observed() (called, resolved bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called, s.resolvedAtCall
}

// TestBuiltinCollectWork_RecordsRepoBeforeUpload pins the ORDER of the manifest
// write against the collect it describes.
//
// The collect's own sink admits the graph into the working set, and the
// background loops woken by that admission ask the manifest whether this machine
// holds the checkout. With the manifest written after the collect, the FIRST
// EVER collect of a repo is therefore evaluated as absent and registers no
// collector until some unrelated graph is next admitted — a real, unsignalled
// window. Asserting the order from inside the collect is what makes that
// unobservable-from-outside sequencing testable.
func TestBuiltinCollectWork_RecordsRepoBeforeUpload(t *testing.T) {
	recordOrderStubOnce.Do(func() { collector.Register(recordOrderStubCollector{}) })
	withTestManifest(t)

	repoDir := t.TempDir()
	repo := filepath.Base(repoDir)

	// KNOWN-NEGATIVE FIRST: nothing has been recorded yet, so the same predicate
	// the sink consults answers false. Without this the sink's later true could
	// just as well be a predicate that always says yes.
	require.False(t, LocalCodeRepoPresent(repo),
		"precondition: the repo must be unknown to the manifest before the collect runs")

	sink := &orderAssertingSink{repo: repo}
	deps := &tailRoutingDeps{routed: &fakeGraphCaller{}, local: &fakeGraphCaller{}}

	_, err := builtinCollectWork(context.Background(), deps,
		collectArgs{Type: recordOrderStubType, ID: repoDir},
		collector.CollectOptions{Sink: sink})
	require.NoError(t, err)

	called, resolvedAtCall := sink.observed()
	require.True(t, called,
		"the sink must actually have been driven — an unrun sink asserts nothing")
	assert.True(t, resolvedAtCall,
		"the manifest entry must already exist when the collect ships its result: the sink's write "+
			"admits the graph, and the background loops woken by that admission decide whether to do "+
			"any work by asking this exact question")
}

// TestBuiltinCollectWork_SkipsManifestForRelativePath pins the guard the moved
// write had to bring with it. In its old position it ran only after
// collector.Collect had already rejected a relative path, so it could assume an
// absolute id; ahead of the collect it cannot, and a relative id would otherwise
// record a name→path pair no consumer could resolve.
func TestBuiltinCollectWork_SkipsManifestForRelativePath(t *testing.T) {
	recordOrderStubOnce.Do(func() { collector.Register(recordOrderStubCollector{}) })
	m := withTestManifest(t)

	_, _ = builtinCollectWork(context.Background(),
		&tailRoutingDeps{routed: &fakeGraphCaller{}, local: &fakeGraphCaller{}},
		collectArgs{Type: recordOrderStubType, ID: "./relative-repo"},
		collector.CollectOptions{Sink: &orderAssertingSink{repo: "relative-repo"}})

	_, ok := m.Lookup("relative-repo")
	assert.False(t, ok, "a relative id must not reach the manifest")

	// CONTROL, same manifest and same call path: an absolute id DOES land, so the
	// absence above is the guard firing rather than the write being broken.
	absDir := t.TempDir()
	_, _ = builtinCollectWork(context.Background(),
		&tailRoutingDeps{routed: &fakeGraphCaller{}, local: &fakeGraphCaller{}},
		collectArgs{Type: recordOrderStubType, ID: absDir},
		collector.CollectOptions{Sink: &orderAssertingSink{repo: filepath.Base(absDir)}})
	_, ok = m.Lookup(filepath.Base(absDir))
	assert.True(t, ok, "control: an absolute id still records")
}
