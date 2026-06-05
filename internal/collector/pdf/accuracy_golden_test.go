package pdf_test

// accuracy_golden_test.go: golden-file schema for the validation
// corpus (T9). Defines the JSON marshaling shape for chunks.golden.json
// and sections.golden.json under collector/pdf/testdata/corpus/<fixture>/.
//
// Decoupled-struct rationale (T9 locked decision #2): chunk.Chunk
// (collector/pdf/chunk/types.go:33-66) has no JSON tags. We define a
// peer goldenChunk struct here with explicit snake_case json tags so
// internal struct renames do not break user-authored golden files.
// Adding json tags directly to chunk.Chunk would couple internal
// evolution to the corpus.
//
// Schema-version field: every golden file carries `"schema_version": 1`
// at the top. Future schema breaks bump the version; loadGoldenChunks
// rejects mismatched versions with a t.Fatalf naming the expected and
// observed values. Pointer-field thresholds (*float64) let unset
// per-fixture overrides fall through to defaultThresholds().

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// schemaVersionCurrent is the only schema_version goldens may carry.
// Bump on any breaking change to goldenFile or goldenSectionFile.
const schemaVersionCurrent = 1

// goldenFile is the on-disk shape of chunks.golden.json. SchemaVersion
// is required; Thresholds is optional (per-fixture override merged
// over defaults); Chunks is the hand-marked chunk array.
type goldenFile struct {
	SchemaVersion int               `json:"schema_version"`
	Thresholds    *goldenThresholds `json:"thresholds,omitempty"`
	Chunks        []goldenChunk     `json:"chunks"`
}

// goldenSectionFile is the on-disk shape of sections.golden.json. Used
// by tests that exercise chunk.ModeSection grouping.
type goldenSectionFile struct {
	SchemaVersion int             `json:"schema_version"`
	Sections      []goldenSection `json:"sections"`
}

// goldenChunk mirrors chunk.Chunk's logical shape with explicit json
// tags. The kind value is the layout.BlockKind string (e.g. "heading",
// "paragraph", "code", "list-item"). PageRange is [first, last]
// 0-indexed inclusive. BBox is [x0, y0, x1, y1] in PDF user-space on
// PageRange[0].
type goldenChunk struct {
	Kind         string        `json:"kind"`
	Text         string        `json:"text"`
	HeadingLevel int           `json:"heading_level,omitempty"`
	PageRange    [2]int        `json:"page_range"`
	BBox         [4]float64    `json:"bbox,omitempty"`
	StructRole   string        `json:"struct_role,omitempty"`
	Children     []goldenChunk `json:"children,omitempty"`
}

// goldenSection mirrors the section-mode chunk shape with title +
// heading level. Authored independently from chunks.golden.json so
// section structure can be hand-marked without re-emitting every
// paragraph chunk.
type goldenSection struct {
	Title     string          `json:"title"`
	Level     int             `json:"level"`
	PageRange [2]int          `json:"page_range"`
	BBox      [4]float64      `json:"bbox,omitempty"`
	Children  []goldenSection `json:"children,omitempty"`
}

// goldenThresholds is the per-fixture threshold override block. All
// fields are pointers so unset overrides fall through to
// defaultThresholds() during mergedThresholds().
//
// Lower-is-better for chunk_count_delta_max and
// reading_order_kendall_tau_max + text_similarity_levenshtein_max
// (these are divergence metrics — a measured value at-or-below the
// max passes). Higher-is-better for boundary_iou_min,
// classification_accuracy_min, heading_level_agreement_min (a
// measured value at-or-above the min passes).
type goldenThresholds struct {
	ChunkCountDeltaMax           *float64 `json:"chunk_count_delta_max,omitempty"`
	BoundaryIoUMin               *float64 `json:"boundary_iou_min,omitempty"`
	ClassificationAccuracyMin    *float64 `json:"classification_accuracy_min,omitempty"`
	HeadingLevelAgreementMin     *float64 `json:"heading_level_agreement_min,omitempty"`
	ReadingOrderKendallTauMax    *float64 `json:"reading_order_kendall_tau_max,omitempty"`
	TextSimilarityLevenshteinMax *float64 `json:"text_similarity_levenshtein_max,omitempty"`
}

// resolvedThresholds is the post-merge concrete threshold set used at
// scoring time. Every field is a plain float64 — no nullable cells.
type resolvedThresholds struct {
	ChunkCountDeltaMax           float64
	BoundaryIoUMin               float64
	ClassificationAccuracyMin    float64
	HeadingLevelAgreementMin     float64
	ReadingOrderKendallTauMax    float64
	TextSimilarityLevenshteinMax float64
}

// defaultThresholds returns the global default thresholds. Per locked
// decision #4: failing-default fixtures override per-fixture; we do
// NOT lower these globally without an explicit ticket.
func defaultThresholds() resolvedThresholds {
	return resolvedThresholds{
		ChunkCountDeltaMax:           0.10,
		BoundaryIoUMin:               0.85,
		ClassificationAccuracyMin:    0.90,
		HeadingLevelAgreementMin:     0.85,
		ReadingOrderKendallTauMax:    0.10,
		TextSimilarityLevenshteinMax: 0.05,
	}
}

// mergedThresholds applies any non-nil per-fixture override over the
// defaults. Returns the concrete resolvedThresholds used by the
// scoring loop.
func mergedThresholds(override *goldenThresholds) resolvedThresholds {
	out := defaultThresholds()
	if override == nil {
		return out
	}
	if override.ChunkCountDeltaMax != nil {
		out.ChunkCountDeltaMax = *override.ChunkCountDeltaMax
	}
	if override.BoundaryIoUMin != nil {
		out.BoundaryIoUMin = *override.BoundaryIoUMin
	}
	if override.ClassificationAccuracyMin != nil {
		out.ClassificationAccuracyMin = *override.ClassificationAccuracyMin
	}
	if override.HeadingLevelAgreementMin != nil {
		out.HeadingLevelAgreementMin = *override.HeadingLevelAgreementMin
	}
	if override.ReadingOrderKendallTauMax != nil {
		out.ReadingOrderKendallTauMax = *override.ReadingOrderKendallTauMax
	}
	if override.TextSimilarityLevenshteinMax != nil {
		out.TextSimilarityLevenshteinMax = *override.TextSimilarityLevenshteinMax
	}
	return out
}

// loadGoldenChunks reads chunks.golden.json at path, validates
// schema_version, and returns the parsed file. Calls t.Fatalf on any
// IO/JSON/version error so callers get a clean failure with the
// fixture name in the message.
func loadGoldenChunks(t *testing.T, path string) goldenFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden chunks %s: %v", path, err)
	}
	var f goldenFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse golden chunks %s: %v", path, err)
	}
	if f.SchemaVersion != schemaVersionCurrent {
		t.Fatalf("golden chunks %s: schema_version %d does not match expected %d (regen the fixture or update the harness)",
			path, f.SchemaVersion, schemaVersionCurrent)
	}
	return f
}

// loadGoldenSections reads sections.golden.json at path, validates
// schema_version, and returns the parsed file.
func loadGoldenSections(t *testing.T, path string) goldenSectionFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden sections %s: %v", path, err)
	}
	var f goldenSectionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse golden sections %s: %v", path, err)
	}
	if f.SchemaVersion != schemaVersionCurrent {
		t.Fatalf("golden sections %s: schema_version %d does not match expected %d (regen the fixture or update the harness)",
			path, f.SchemaVersion, schemaVersionCurrent)
	}
	return f
}

// parseGoldenChunksBytes is the in-memory variant used by self-tests
// and schema-rejection tests. Returns an error rather than calling
// t.Fatalf so the caller can assert on the message.
func parseGoldenChunksBytes(raw []byte) (goldenFile, error) {
	var f goldenFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("parse golden chunks: %w", err)
	}
	if f.SchemaVersion != schemaVersionCurrent {
		return f, fmt.Errorf("schema_version %d does not match expected %d", f.SchemaVersion, schemaVersionCurrent)
	}
	return f, nil
}

// TestGoldenSchema_VersionRejection verifies that schema_version: 2
// (or anything != 1) produces a parse error mentioning the version
// mismatch.
func TestGoldenSchema_VersionRejection(t *testing.T) {
	t.Parallel()
	bad := []byte(`{"schema_version": 2, "chunks": []}`)
	_, err := parseGoldenChunksBytes(bad)
	if err == nil {
		t.Fatalf("expected schema_version mismatch error, got nil")
	}
	msg := err.Error()
	// The error message must mention both the bad version (2) and the
	// expected version (schemaVersionCurrent = 1) so the human reading
	// the failure understands what to fix.
	if !contains(msg, "schema_version") || !contains(msg, "2") || !contains(msg, "1") {
		t.Errorf("error message %q must mention schema_version, 2, and 1", msg)
	}
}

// TestGoldenSchema_SectionsRoundTrip exercises the parallel sections
// golden path so the goldenSectionFile + goldenSection types stay
// honest under lint and ready for the section-mode harness consumer.
func TestGoldenSchema_SectionsRoundTrip(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := tmp + "/sections.golden.json"
	raw := []byte(`{
		"schema_version": 1,
		"sections": [
			{ "title": "Top", "level": 1, "page_range": [0, 0],
			  "children": [ { "title": "Sub", "level": 2, "page_range": [0, 0] } ] }
		]
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	f := loadGoldenSections(t, path)
	if f.SchemaVersion != 1 || len(f.Sections) != 1 {
		t.Fatalf("got %+v", f)
	}
	if f.Sections[0].Title != "Top" || len(f.Sections[0].Children) != 1 {
		t.Fatalf("section walk: got %+v", f.Sections)
	}
	if f.Sections[0].Children[0].Level != 2 {
		t.Errorf("nested level: got %d want 2", f.Sections[0].Children[0].Level)
	}
}

// TestGoldenSchema_RoundTrip verifies a hand-authored golden file
// parses cleanly with non-nil thresholds and one chunk.
func TestGoldenSchema_RoundTrip(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"schema_version": 1,
		"thresholds": { "boundary_iou_min": 0.80 },
		"chunks": [
			{
				"kind": "paragraph",
				"text": "Hello world.",
				"page_range": [0, 0],
				"bbox": [10, 700, 200, 720]
			}
		]
	}`)
	f, err := parseGoldenChunksBytes(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.SchemaVersion != 1 {
		t.Errorf("schema_version: got %d want 1", f.SchemaVersion)
	}
	if len(f.Chunks) != 1 || f.Chunks[0].Text != "Hello world." {
		t.Errorf("chunks: got %+v", f.Chunks)
	}
	merged := mergedThresholds(f.Thresholds)
	if merged.BoundaryIoUMin != 0.80 {
		t.Errorf("merged BoundaryIoUMin: got %v want 0.80", merged.BoundaryIoUMin)
	}
	// Unset thresholds fall through to defaults.
	if merged.ChunkCountDeltaMax != 0.10 {
		t.Errorf("merged ChunkCountDeltaMax: got %v want 0.10 (default)", merged.ChunkCountDeltaMax)
	}
}

// contains is a one-line wrapper around strings.Contains; declared
// here to avoid pulling strings into this file's import set when the
// only need is the test above. Kept private to package pdf_test.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
