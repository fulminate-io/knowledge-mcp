// SPDX-License-Identifier: Apache-2.0

package hnsw

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
// IT REPLACES THE DELETED Format.Merge. That method returned a Segment because
// it materialized one; MergeTo reports a length instead, and the assembling of a
// Segment from those bytes moved to the engine. Every test that wanted "the
// merged segment" still wants it, so the assembly is reproduced here rather than
// each call site growing five lines of file plumbing.
//
// IT IS NOT A SUBSTITUTE FOR THE ENGINE'S OWN PATH, and no test should read it
// as one: it deliberately does NOT map the file, because these are format tests
// and the mapping is the distribution layer's concern. The engine's real
// lifecycle — including the unlink and the mapping — is exercised by
// TestMergeLeavesNoScratchFileOnSuccessOrError and
// TestMergedPayloadIsMappingBackedNotHeapBacked.
func mergeSegments(
	t *testing.T, segs []searchengine.Segment[[]byte, struct{}],
	accept []func(searchengine.ExternalID) bool,
) (searchengine.Segment[[]byte, struct{}], error) {
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
