// SPDX-License-Identifier: Apache-2.0

// Package logs — concentration heuristic tests.
package logs

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// buildAggForConcentration constructs an AggregationSummary populated
// with the given (key, value, count) tuples. Each tuple becomes one
// LabelValueStats with TotalCount=count, severities ignored — the
// concentration heuristic only looks at totals.
func buildAggForConcentration(t *testing.T, tuples []struct {
	key   string
	value string
	count int
}) *AggregationSummary {
	t.Helper()
	agg := &AggregationSummary{stats: make(map[string]map[string]*LabelValueStats)}
	for _, tu := range tuples {
		s := agg.getOrCreate(tu.key, tu.value)
		s.TotalCount += tu.count
	}
	return agg
}

// TestFindConcentrations_DominantValueFlagged asserts a label value
// claiming >50% of total entries is reported as concentrated.
func TestFindConcentrations_DominantValueFlagged(t *testing.T) {
	agg := buildAggForConcentration(t, []struct {
		key   string
		value string
		count int
	}{
		{"node", "A", 80},
		{"node", "B", 20},
		{"reason", "OOMKilled", 60},
		{"reason", "ImagePull", 40},
	})
	findings := findConcentrations(agg, 100)
	require.Len(t, findings, 2)
	// node=A (80%) should outrank reason=OOMKilled (60%).
	assert.Equal(t, "node", findings[0].Key)
	assert.Equal(t, "A", findings[0].Value)
	assert.InDelta(t, 0.8, findings[0].Share, 1e-9)
	assert.Equal(t, "reason", findings[1].Key)
	assert.Equal(t, "OOMKilled", findings[1].Value)
	assert.InDelta(t, 0.6, findings[1].Share, 1e-9)
}

// TestFindConcentrations_NoDominantValue asserts that when no value
// crosses the 50% threshold, nothing is flagged. We don't want to
// surface noise.
func TestFindConcentrations_NoDominantValue(t *testing.T) {
	agg := buildAggForConcentration(t, []struct {
		key   string
		value string
		count int
	}{
		{"node", "A", 30},
		{"node", "B", 30},
		{"node", "C", 40},
	})
	findings := findConcentrations(agg, 100)
	assert.Empty(t, findings, "no value above 50% → no findings")
}

// TestFindConcentrations_SkipsClusterAndProject asserts the
// uninteresting keys (cluster_name, project_id) are not reported even
// when fully concentrated. They're degenerate by construction in
// single-cluster collections.
func TestFindConcentrations_SkipsClusterAndProject(t *testing.T) {
	agg := buildAggForConcentration(t, []struct {
		key   string
		value string
		count int
	}{
		{"cluster_name", "main", 100},
		{"project_id", "foo", 100},
		{"node", "A", 80},
		{"node", "B", 20},
	})
	findings := findConcentrations(agg, 100)
	require.Len(t, findings, 1, "only 'node' should surface")
	assert.Equal(t, "node", findings[0].Key)
}

// TestFindConcentrations_ZeroTotal asserts a zero-entry collection
// returns no findings rather than dividing by zero.
func TestFindConcentrations_ZeroTotal(t *testing.T) {
	agg := buildAggForConcentration(t, []struct {
		key   string
		value string
		count int
	}{
		{"node", "A", 0},
	})
	assert.Empty(t, findConcentrations(agg, 0))
}

// TestWriteConcentrationSection_RendersOrSilent verifies the emitted
// markdown shape and that the section is omitted entirely when there
// are no findings.
func TestWriteConcentrationSection_RendersOrSilent(t *testing.T) {
	agg := buildAggForConcentration(t, []struct {
		key   string
		value string
		count int
	}{
		{"node", "A", 90},
		{"node", "B", 10},
	})
	var sb strings.Builder
	writeConcentrationSection(&sb, agg, 100)
	got := sb.String()
	assert.Contains(t, got, "Concentration", "header should render")
	assert.Contains(t, got, "node=A", "key=value pair should render")
	assert.Contains(t, got, "90 of 100", "count fraction should render")
	assert.Contains(t, got, "(90%)", "percentage should render")

	// No findings → no section.
	flatAgg := buildAggForConcentration(t, []struct {
		key   string
		value string
		count int
	}{
		{"node", "A", 50},
		{"node", "B", 50},
	})
	var sb2 strings.Builder
	writeConcentrationSection(&sb2, flatAgg, 100)
	assert.Empty(t, sb2.String(), "no findings → no section emitted")
}

// TestBuildSummary_IncludesConcentration verifies the end-to-end
// summary path includes the concentration section between header and
// top patterns.
func TestBuildSummary_IncludesConcentration(t *testing.T) {
	tmpl := &wirelogs.LogTemplate{ID: "t1", Pattern: "boom", Severity: wirelogs.SeverityError, Count: 100}
	stream := &wirelogs.LogStream{ID: "s1", Labels: map[string]string{"node": "A"}}
	chunk := &wirelogs.LogChunk{ID: "c1", StreamID: "s1", TemplateID: "t1", EntryCount: 100,
		StartTime: time.Now(), EndTime: time.Now()}
	agg := BuildAggregationSummary([]*wirelogs.LogStream{stream}, []*wirelogs.LogChunk{chunk},
		map[string]*wirelogs.LogTemplate{"t1": tmpl})

	summary := buildSummary(
		[]*wirelogs.LogTemplate{tmpl},
		[]*wirelogs.LogStream{stream},
		[]*wirelogs.LogChunk{chunk},
		nil, // no correlations
		agg,
		wirelogs.Query{},
	)
	assert.Contains(t, summary, "Concentration",
		"summary should include concentration section")
	assert.Contains(t, summary, "node=A",
		"the dominant label value should appear")
	// Concentration section comes BEFORE Top Error Patterns.
	idxConc := strings.Index(summary, "Concentration")
	idxPat := strings.Index(summary, "Top Error Patterns")
	require.GreaterOrEqual(t, idxConc, 0)
	require.GreaterOrEqual(t, idxPat, 0)
	assert.Less(t, idxConc, idxPat,
		"concentration should render before top patterns")
}
