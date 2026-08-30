// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// mergeSegments consolidates segs and returns the merged Segment, doing on the
// test's behalf exactly what the ENGINE does in production: create a
// destination, call MergeTo, size the file from the reported length, and decode
// the result.
//
// IT REPLACES THE DELETED Format.Merge, for the reason recorded on the hnsw
// helper of the same name: MergeTo reports a length rather than materializing a
// Segment, and the assembly moved to the engine, so tests that want the merged
// segment reproduce that assembly in one place instead of at every call site.
//
// It takes []searchengine.Segment, which is what the format-level call sites
// hold. Tests working with *mappedSegment inputs and a chosen dictionary kind
// use mergeToSegment instead — MergeTo emits defaultDictKind only.
func mergeSegments(
	t *testing.T, segs []searchengine.Segment[Query, *CorpusStats],
	accept []func(searchengine.ExternalID) bool,
) (searchengine.Segment[Query, *CorpusStats], error) {
	t.Helper()

	f, err := os.Create(filepath.Join(t.TempDir(), "merged.seg")) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("creating the merge destination: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("closing the merge destination: %v", err)
		}
	}()

	n, err := Format{}.MergeTo(f, segs, accept)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(n); err != nil {
		t.Fatalf("sizing the merge destination to %d: %v", n, err)
	}
	blob, err := os.ReadFile(f.Name()) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("reading the merged segment back: %v", err)
	}
	return Format{}.Decode(blob)
}
