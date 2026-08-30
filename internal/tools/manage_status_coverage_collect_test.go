// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_collect_test.go — tests for the coverage table's ASSEMBLY
// half: the Stats fan-out, the JSON row shape it produces, and the markdown table
// rendered from those rows. Split from manage_status_coverage_test.go for the
// 500-line cap, mirroring the production split. The shared fixtures (coverageFake,
// coverageSegReader, coverageDeps) stay in the sibling and are used from here — same
// package, so nothing is duplicated.
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestCollectCoverageRows_JSONShape is the step criterion for the coverage[] JSON
// block: rows serialize with the PINNED snake_case keys the web types against, the
// graph-label field serializes as `graph` (not `label`), a segment graph whose
// live resident is below its covered count surfaces live_resident < seg_covered
// (the web's live-0 WARN lever), and a graph with no segment pool carries
// has_segments=false so the web renders '—' instead of 'shipped 0 · live 0'.
func TestCollectCoverageRows_JSONShape(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge":   {NonProxyNodeCount: 10, SummarizedCount: 4, BinaryVectorCount: 4, SummaryFailureCount: 1, EmbedFailureCount: 2},
		"code/myrepo": {NonProxyNodeCount: 8, SummarizedCount: 8, BinaryVectorCount: 8},
		"practice/go": {NonProxyNodeCount: 20, SummarizedCount: 20, BinaryVectorCount: 12},
	}}
	// code/myrepo: segments cover 6 of 8 embedded but the LIVE engine is resident
	// with only 2 — a collapse the web WARNs on (live_resident < seg_covered).
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"knowledge": 0, "code/myrepo": 6, "practice/go": 0},
		residentByKey: map[string]int{"knowledge": 0, "code/myrepo": 2, "practice/go": 0},
	}
	rows := collectCoverageRows(context.Background(), &coverageDeps{gc: fake, segCov: seg})
	require.NotEmpty(t, rows)

	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	// Every row carries EXACTLY the pinned snake_case keys — `graph` (not `label`).
	wantKeys := []string{"graph", "total", "summarized", "embedded", "seg_covered", "live_resident", "has_segments", "summary_fail", "embed_fail"}
	for i, row := range decoded {
		for _, k := range wantKeys {
			_, present := row[k]
			assert.True(t, present, "row %d missing key %q", i, k)
		}
		_, hasLabel := row["label"]
		assert.False(t, hasLabel, "row %d must serialize the graph-label field as `graph`, not `label`", i)
	}

	byGraph := map[string]map[string]any{}
	for _, row := range decoded {
		byGraph[row["graph"].(string)] = row
	}

	// knowledge row carries the failure counts under snake_case keys.
	k := byGraph["knowledge"]
	require.NotNil(t, k)
	assert.EqualValues(t, 1, k["summary_fail"])
	assert.EqualValues(t, 2, k["embed_fail"])

	// code/myrepo: segment-bearing, live resident collapsed below covered.
	code := byGraph["code/myrepo"]
	require.NotNil(t, code)
	assert.Equal(t, true, code["has_segments"])
	assert.EqualValues(t, 6, code["seg_covered"])
	assert.EqualValues(t, 2, code["live_resident"])
	assert.Less(t, code["live_resident"].(float64), code["seg_covered"].(float64),
		"a collapsed live pool must surface live_resident < seg_covered for the web WARN")

	// A graph with no rebuildable segments carries has_segments=false so the web
	// renders '—' rather than 'shipped 0 · live 0'.
	nonSeg := newCoverageRow("linkage", &knowledgev1.GraphStats{NonProxyNodeCount: 3},
		0, 0, false, false, true, false, 0, 0, 0)
	nsRaw, err := json.Marshal(nonSeg)
	require.NoError(t, err)
	var ns map[string]any
	require.NoError(t, json.Unmarshal(nsRaw, &ns))
	assert.Equal(t, false, ns["has_segments"], "a non-segment graph serializes has_segments=false")
}

// TestRenderLLMCoverage_Table pins the per-graph coverage rendering:
//   - the knowledge row is present even though its enumerated name is empty (T3-2)
//   - a fully-covered code graph renders distinctly from a 0-of-N knowledge graph
//   - the auto-summary caption is present (T3-1)
//   - every StatsRequest the renderer issued carried IncludeCoverage==true (T2)
func TestRenderLLMCoverage_Table(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		// knowledge: 10 nodes, 0 summarized → "0 of 10" (never-ran-on-code shape)
		"knowledge": {NonProxyNodeCount: 10, SummarizedCount: 0, BinaryVectorCount: 0},
		// code/myrepo: fully covered 8 of 8 + 8 embedded, no failures
		"code/myrepo": {NonProxyNodeCount: 8, SummarizedCount: 8, BinaryVectorCount: 8},
		// practice/go: a NON-code embeddable builtin — 20 nodes, 12 embedded. Its
		// segment coverage now surfaces as a real cell instead of "—".
		"practice/go": {NonProxyNodeCount: 20, SummarizedCount: 20, BinaryVectorCount: 12},
	}}
	// Segment-coverage stub: code/myrepo ships 6 docs against its 8 embedded (a
	// degenerate-looking pool, the lever-3 operator signal) and the live engine is
	// resident with all 6 (a healthy live≈shipped row); knowledge ships nothing so
	// its segment cell is "shipped 0 · live 0". practice/go ships nothing against 12
	// embedded docs (a never-shipped non-code graph) — zero shown as a real number,
	// "shipped 0 · live 0", not "—".
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"knowledge": 0, "code/myrepo": 6, "practice/go": 0},
		residentByKey: map[string]int{"knowledge": 0, "code/myrepo": 6, "practice/go": 0},
	}
	deps := &coverageDeps{gc: fake, segCov: seg}

	out := renderLLMCoverage(context.Background(), deps)

	// Header + caption (T3-1).
	assert.Contains(t, out, "LLM coverage")
	assert.Contains(t, out, "deterministic auto-summaries",
		"the summarized-semantics caption must be present")
	// Segment-coverage column header (lever 3).
	assert.Contains(t, out, "segment coverage", "the segment-coverage column header must be present")

	// Knowledge row present despite empty enumerated name (T3-2).
	assert.Contains(t, out, "| knowledge |", "knowledge row must render via the explicit empty-name selector")
	// Code row present via enumeration.
	assert.Contains(t, out, "| code/myrepo |")

	// 0-of-N distinct from N-of-N: knowledge is "0 of 10", code is "8 of 8".
	assert.Contains(t, out, "0 of 10", "never-summarized knowledge graph renders 0 of N")
	assert.Contains(t, out, "8 of 8", "fully-covered code graph renders N of N")

	// Segment coverage renders the shipped and live counts as labeled terms for the
	// code graph; a healthy row shows live≈shipped.
	assert.Contains(t, out, "shipped 6 · live 6", "code graph renders shipped and live as labeled terms")

	// lever-3 surface: a NON-code embeddable builtin (practice/go) renders a REAL
	// segment-coverage cell — zero coverage shown as real numbers, not "—" or an
	// omitted row. segCoveredFor gates on HasRebuildableSegments, so
	// practice/cloud/cicd report coverage.
	//
	// This asserts the WHOLE ROW rather than the cell alone, and that is load-bearing
	// now that the embedded count lives only in its own column: the cell for a
	// never-shipped graph reads "shipped 0 · live 0" whatever its embedded count is,
	// so knowledge (0 embedded) and practice/go (12 embedded) have IDENTICAL cells and
	// a cell-only assertion could be satisfied by the wrong row. The full row carries
	// the embedded column that tells them apart.
	assert.Contains(t, out, "| practice/go |", "a non-code embeddable graph renders its own row")
	assert.Contains(t, out, "| practice/go | 20 | 20 of 20 | 12 of 20 | shipped 0 · live 0 [below-floor] | 0 | 0 |",
		"practice graph renders zero segment coverage as real numbers plus its real embedded count, not the — placeholder")

	// T2: every issued StatsRequest set IncludeCoverage.
	require.NotEmpty(t, fake.reqs, "renderer must issue at least one Stats RPC")
	for i, r := range fake.reqs {
		assert.True(t, r.GetIncludeCoverage(), "StatsRequest %d must set IncludeCoverage (the coverage trigger)", i)
	}
}

// TestRenderLLMCoverage_ShippedLiveGrammar pins the segment cell's THREE-COUNT
// grammar: the cell names the shipped and live counts as separately labeled terms
// and never joins them with "of".
//
// The defect this catches is a FALSE CONTAINMENT. SegCovered sums HNSW doc_counts
// across the SHIPPED manifest, in which superseded and hard-deleted generations
// survive; Embedded counts live graph nodes holding a binary vector; and the
// residue band is DEFINED as LiveResident > Embedded. No one of the three bounds
// another, so "N of M" asserted a subset relation that does not exist — a churned
// but perfectly converged graph rendered "4695 of 781".
//
// The fixtures are the two shapes measured on the live daemon, not invented ones:
// code/myrepo carries the code/summ-eval reading (shipped 4695 against 781
// embedded, fully converged) and practice/go the cloud/fulminate-data reading
// (shipped 92, embedded 14, live 106 — the residue band, where live EXCEEDS
// embedded and the old grammar's denominator was smaller than both other terms).
// knowledge is the healthy control: a graph whose three counts genuinely agree
// still renders the same grammar, so the fix is not special-casing the skewed rows.
//
// Each absence assertion is paired with the presence assertion on the SAME numbers,
// which is what keeps it from passing vacuously against a renderer that dropped the
// cell entirely.
func TestRenderLLMCoverage_ShippedLiveGrammar(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		// The healthy control — three counts in agreement.
		"knowledge": {NonProxyNodeCount: 100, SummarizedCount: 100, BinaryVectorCount: 100},
		// The summ-eval shape: shipped MULTIPLES of embedded, and converged.
		"code/myrepo": {NonProxyNodeCount: 788, SummarizedCount: 788, BinaryVectorCount: 781},
		// The residue shape: live EXCEEDS embedded.
		"practice/go": {NonProxyNodeCount: 109, SummarizedCount: 109, BinaryVectorCount: 14},
	}}
	seg := &coverageSegReader{
		coveredByKey: map[string]int{
			"knowledge/default": 100, "code/myrepo": 4695, "practice/go": 92,
		},
		residentByKey: map[string]int{
			"knowledge/default": 100, "code/myrepo": 781, "practice/go": 106,
		},
	}

	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake, segCov: seg})

	// The summ-eval row: shipped 4695 against 781 live, and CONVERGED — the exact
	// reading that rendered as the nonsense "4695 of 781 (live 781) [converged]".
	assert.Contains(t, out, "shipped 4695 · live 781 [converged]",
		"the shipped and live counts are separately labeled terms, not a ratio")
	assert.NotContains(t, out, "4695 of 781",
		"the shipped count is not 'of' the embedded count — nothing bounds anything here")

	// The residue row, where the old grammar's denominator (14) was smaller than
	// BOTH other terms — the shape that made the containment claim absurd.
	assert.Contains(t, out, "shipped 92 · live 106 [residue]",
		"the residue band renders the same three-count grammar")
	assert.NotContains(t, out, "92 of 14", "the residue row must not claim 92 is a subset of 14")

	// The healthy control: agreeing counts get no special treatment.
	assert.Contains(t, out, "shipped 100 · live 100 [converged]",
		"a graph whose counts agree renders the same grammar as a skewed one")

	// The embedded count is still REPORTED — it simply lives in the table's own
	// embedded column rather than being duplicated into the segment cell. Without
	// this the fix would be indistinguishable from silently dropping the measurement.
	assert.Contains(t, out, "| 781 of 788 |",
		"embedded-of-total keeps its own column; the segment cell must not duplicate it")
}

// TestRenderLLMCoverage_LiveResidentCollapse is the masking-fix criterion: a graph
// whose server-shipped corpus is intact (shipped=N) but whose LIVE engine resident
// has collapsed to 0 renders a cell surfacing both — "shipped N · live 0" — so the
// post-restart collapse is visible instead of masked behind the intact shipped
// figure. Dropping the live term makes the "live 0" assertion fail.
func TestRenderLLMCoverage_LiveResidentCollapse(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge":   {NonProxyNodeCount: 10, SummarizedCount: 10, BinaryVectorCount: 10},
		"code/myrepo": {NonProxyNodeCount: 80, SummarizedCount: 80, BinaryVectorCount: 80},
	}}
	// code/myrepo: server holds the full corpus (covered=80) but the live searchable
	// pool has collapsed (resident=0) — the masked post-restart incident.
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"knowledge": 0, "code/myrepo": 80},
		residentByKey: map[string]int{"knowledge": 0, "code/myrepo": 0},
	}
	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake, segCov: seg})

	assert.Contains(t, out, "shipped 80 · live 0",
		"a collapsed live pool surfaces live 0 against the intact shipped corpus — the masking fix")
}

// TestRenderLLMCoverage_EmptyGraph pins the (empty graph) rendering for a
// zero-denominator graph — visibly distinct from a covered graph — and that the
// empty row keeps the 7-column alignment after the segment-coverage column was
// added (a trailing empty cell, not a short row).
func TestRenderLLMCoverage_EmptyGraph(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge": {NonProxyNodeCount: 0},
	}}
	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake})
	assert.Contains(t, out, "(empty graph)", "a zero-denominator graph renders (empty graph)")
	// 7-column alignment: label + (empty graph) + 5 empty cells = 8 pipes.
	assert.Contains(t, out, "| knowledge | (empty graph) | | | | | |",
		"the empty-graph row keeps the segment-coverage column's alignment")
}

// TestRenderLLMCoverage_SegmentPlaceholder pins the "—" placeholder: when the
// SegmentCoverage seam is unwired (degraded headless mode), a segment-bearing
// graph's segment-coverage cell renders the placeholder rather than a number or a
// crash.
func TestRenderLLMCoverage_SegmentPlaceholder(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge":   {NonProxyNodeCount: 10, SummarizedCount: 3, BinaryVectorCount: 4},
		"code/myrepo": {NonProxyNodeCount: 8, SummarizedCount: 8, BinaryVectorCount: 8},
	}}
	// segCov nil — the degraded/headless path.
	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake})
	assert.Contains(t, out, "—", "an unwired segment seam renders the placeholder, not a number")
}

// TestRenderLLMCoverage_KnowledgeRowProbesDefaultInstance is the instrument
// criterion for the primary corpus: the knowledge row's segment-coverage cell
// must report the coverage of the instance the segments ACTUALLY live under.
//
// The default knowledge graph is keyed "default" everywhere segments are
// produced and reconciled — the reconcile loop seeds {GraphKnowledge, "default"}
// explicitly, precisely because the default instance enumerates an empty name
// that ListGraphNamesOfType drops. The coverage table's Stats selector correctly
// uses the empty-name form (that is the STATS wire contract), but the segment
// probe is a different key space and needs the instance name.
//
// Probing the empty name there reads a key nothing writes, so the primary
// corpus's coverage is reported as zero (or, when the empty-key probe errors,
// as the "—" placeholder) no matter how much of it is really covered — the one
// graph most likely to sit in a coverage hole is the one the instrument cannot
// see. This test seeds coverage ONLY under the default key and asserts the row
// reports it; it fails under both symptom shapes.
//
// The second assertion is the side-effect guard. The real reader's
// ResidentDocCount lazily constructs a per-graph manager (and its on-disk L2
// directory) for whatever key it is handed, so probing the empty name makes a
// status READ create state for an instance that does not exist. Asserting the
// empty-name key is never probed closes that at the caller, where the wrong key
// originated.
func TestRenderLLMCoverage_KnowledgeRowProbesDefaultInstance(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		"knowledge": {NonProxyNodeCount: 8400, SummarizedCount: 8400, BinaryVectorCount: 8400},
	}}
	// Coverage exists ONLY under the default instance key — where the reconcile
	// seeds it and where the blobs are stored. Nothing is seeded under the
	// bare-type (empty-name) key, so a probe using that key reads zero.
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"knowledge/default": 5220},
		residentByKey: map[string]int{"knowledge/default": 5220},
	}

	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake, segCov: seg})

	assert.Contains(t, out, "shipped 5220 · live 5220",
		"the knowledge row must report the default instance's real coverage, not a "+
			"zero read off an instance key nothing writes")
	assert.Contains(t, seg.probedKeys(), "knowledge/default",
		"the knowledge row must probe segment coverage under the default instance key")
	assert.NotContains(t, seg.probedKeys(), "knowledge",
		"the empty-name key must never be probed — the real reader lazily constructs a "+
			"manager and an L2 directory for whatever key it is handed, so probing a "+
			"nonexistent instance makes a status READ create state")
}
