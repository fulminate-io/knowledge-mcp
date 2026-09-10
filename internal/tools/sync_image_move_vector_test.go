// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// imageMoveVector is the shared table at testdata/graph_image_move_selector_cases.json:
// which selector field addresses a graph when a whole graph IMAGE is moved.
//
// IT IS A DATA FILE RATHER THAN A SHARED PACKAGE because the modules that move
// images cannot import each other — the only hand-written contract between them
// is generated protobuf. The bench module reads the SAME rows through its own
// test, so a drift on either side reds on that side instead of surfacing as a
// transfer that quietly moved the wrong graph.
type imageMoveVector struct {
	Spec  string `json:"spec"`
	Cases []struct {
		Label        string `json:"label"`
		GraphType    string `json:"graph_type"`
		InstanceName string `json:"instance_name"`
		Field        string `json:"field"`
		Value        string `json:"value"`
	} `json:"cases"`
}

// sharedVectorPath returns the IN-MODULE path to a shared vector.
//
// IT IS DELIBERATELY NOT A WALK UP TO THE REPO ROOT, and that is the whole point
// of this function existing rather than an inline filepath.Join. The Go test
// cache hashes a file a test opens ONLY when the file lives under the tested
// package's module root: an open that leaves the module is dropped from the
// input id, so the package keeps reporting a cached PASS after that file changes
// — which is exactly the drift a shared vector exists to catch. The entry under
// this package's own testdata is a relative SYMLINK to the repo-root file, so
// there is still one authored copy of the table and the cache can still see it.
//
// The link is pinned by TestImageMoveVectorIsASymlinkToTheSharedTable below; a
// reader that quietly turned into a second copy of the rows would defeat the
// single-source half while keeping the cache half.
func sharedVectorPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name)
	_, err := os.Stat(path)
	require.NoErrorf(t, err, "the in-module vector entry testdata/%s must exist and resolve", name)
	return path
}

// TestImageMoveVectorIsASymlinkToTheSharedTable pins the mechanism the reader
// above depends on: the in-module entry is a LINK to the one authored table, not
// a copy of it. A copy would read identically today and drift silently tomorrow,
// which is the failure the shared table exists to prevent.
//
// BOTH PATHS ARE MADE ABSOLUTE BEFORE THEY ARE RESOLVED. filepath.EvalSymlinks
// preserves the relative-ness of its input, so it never walks links in
// directories ABOVE the working directory; comparing a resolved relative path
// against a resolved absolute one fails on a platform whose temp or checkout
// path is itself a link.
func TestImageMoveVectorIsASymlinkToTheSharedTable(t *testing.T) {
	entry := filepath.Join("testdata", "graph_image_move_selector_cases.json")
	info, err := os.Lstat(entry)
	require.NoError(t, err)
	require.NotZerof(t, info.Mode()&os.ModeSymlink,
		"%s must be a symlink to the repo-root table, not a copy of it", entry)

	absEntry, err := filepath.Abs(entry)
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(absEntry)
	require.NoError(t, err, "the link must resolve — a dangling entry is a test that cannot run")

	absRoot, err := filepath.Abs(sharedTableRoot(t, "graph_image_move_selector_cases.json"))
	require.NoError(t, err)
	wantRoot, err := filepath.EvalSymlinks(absRoot)
	require.NoError(t, err)
	require.Equal(t, wantRoot, resolved, "the link must resolve to the one authored copy of the table")
}

func loadImageMoveVector(t *testing.T) imageMoveVector {
	t.Helper()
	raw, err := os.ReadFile(sharedVectorPath(t, "graph_image_move_selector_cases.json"))
	require.NoError(t, err)
	var v imageMoveVector
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Cases, "the shared vector carries rows")
	return v
}

// instanceFieldsOf projects every instance field a selector can carry, so a case
// can assert the ONE field it names and the ABSENCE of the other three in the
// same breath. Asserting only the named field would pass a builder that set two.
func instanceFieldsOf(sel *knowledgev1.GraphSelector) map[string]string {
	return map[string]string{
		"repo":     sel.GetRepo(),
		"account":  sel.GetAccount(),
		"name":     sel.GetName(),
		"language": sel.GetLanguage(),
	}
}

// TestSyncGraphSelector_MatchesTheSharedImageMoveVector drives the client's
// image-move selector builder over every row of the shared table.
//
// FAILS WHEN ABSENT: drop the instance name in syncGraphSelector's non-practice
// fall-through and the code, cloud, cicd and logs rows go red here; fold a legacy
// practice name back into the singleton and the practice/go row goes red. Both
// are the same defect the ticket exists to fix — an image move addressing a graph
// the caller did not name — and before this test only the practice half of it was
// observed anywhere in the package.
func TestSyncGraphSelector_MatchesTheSharedImageMoveVector(t *testing.T) {
	v := loadImageMoveVector(t)
	for _, c := range v.Cases {
		t.Run(c.Label, func(t *testing.T) {
			sel := syncGraphSelector(c.GraphType, c.InstanceName)
			require.NotNil(t, sel)
			assert.Equal(t, c.GraphType, sel.GetGraph(), "the selector names the family")

			for field, got := range instanceFieldsOf(sel) {
				if field == c.Field {
					assert.Equal(t, c.Value, got, "%s: the %s field carries the instance", c.Label, field)
					continue
				}
				assert.Empty(t, got,
					"%s: %s must stay empty — a selector carrying a field its family does not consume is refused", c.Label, field)
			}
		})
	}
}

// TestSyncPush_NonPracticeFamiliesCarryTheirInstanceName is the ARM-level half of
// the row above: the same rule read off the real InterceptSync push arm rather
// than off the builder, because the builder being right is worth nothing if the
// arm stops calling it.
//
// EVERY SUBTEST SUPPLIES A NON-EMPTY NAME. That is the whole discriminating
// power: with an empty name a builder that drops the instance and one that
// carries it agree, which is how the previous round left this fall-through
// unobserved while every pre-existing sync push test pushed knowledge/default.
func TestSyncPush_NonPracticeFamiliesCarryTheirInstanceName(t *testing.T) {
	for _, tc := range []struct {
		graph, name, field string
	}{
		{graph: "code", name: "knowledge-repo", field: "repo"},
		// A LEGACY PRACTICE IMAGE, which is the one remaining family whose image
		// move carries an instance name that is not a repo: the combined graph is a
		// singleton for reads and writes, but the pre-singleton images still exist
		// and are addressed on `language`. It replaces the account-keyed row, whose
		// family retired with its built-in collector.
		{graph: "practice", name: "go", field: "language"},
		{graph: "web", name: "site-alpha", field: "name"},
	} {
		t.Run(tc.graph, func(t *testing.T) {
			backend := newFakeSyncBackend(t)
			withFakeSyncTransport(t, backend)

			exp := &fakeExporter{bytesOut: []byte("KGV4 " + tc.graph + "/" + tc.name)}
			handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp},
				syncParams(t, map[string]any{"operation": "push", "graph": tc.graph, "name": tc.name}))
			require.True(t, handled)

			// web is refused by the syncable gate BEFORE any selector is built —
			// the raw graphs stay on this machine — so its arm result is the
			// refusal, and asserting a selector for it would assert nothing. It is
			// the row that keeps the name-keyed refusal observed now that the log
			// family it used to be spelled with is retired.
			if tc.graph == "web" {
				require.True(t, out.IsError, "a raw graph is not sync-eligible")
				assert.Equal(t, 0, exp.exportCalls, "the refusal precedes the export")
				return
			}

			require.False(t, out.IsError, "push %s/%s: %q", tc.graph, tc.name, textOf(out))
			require.Equal(t, 1, exp.exportCalls)
			require.NotNil(t, exp.lastTarget)
			for field, got := range instanceFieldsOf(exp.lastTarget) {
				if field == tc.field {
					assert.Equal(t, tc.name, got,
						"the export target carries the instance on %s — dropping it exports the wrong graph", field)
					continue
				}
				assert.Empty(t, got, "%s must stay empty for graph=%s", field, tc.graph)
			}
			assert.Equal(t, tc.name, backend.lastPresignName, "and the cloud object is offered under that name")
		})
	}
}

// sharedTableRoot returns the path of the ONE authored copy of a shared vector:
// the first directory at or above this package that holds BOTH a go.mod and
// testdata/<name>.
//
// IT IS THE PIN'S EXPECTATION, NEVER THE READER'S PATH. The reader deliberately
// stays inside this module so the table is a test-cache input; this walk exists
// only to name what the in-module link must point AT, and it is written as a walk
// rather than as a fixed number of "..", because the depth differs by layout: in
// the staging repo this package sits under cmd/knowledge and the table is at the
// repo root, while in the published mirror the client module IS the root. A
// hard-coded depth passes here and fails there, which is exactly what the mirror
// staging run caught.
func sharedTableRoot(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			candidate := filepath.Join(dir, "testdata", name)
			if _, statErr := os.Stat(candidate); statErr == nil {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir,
			"walked to the filesystem root without finding testdata/%s beside a go.mod", name)
		dir = parent
	}
}
