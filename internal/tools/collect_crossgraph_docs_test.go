// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_crossgraph_docs_test.go — the collector guide documents the
// target-graph field and, just as load-bearing, WHICH OF THE TWO MECHANISMS a
// collector author should reach for.
//
// WHY THE PROSE GETS A GATE. The guide states that a field not in its tables is
// an ERROR, so a field the tables do not carry is a field a collector author is
// told not to use — the carrier would ship undocumented and unusable. And the
// two mechanisms land in DIFFERENT GRAPHS: a target-graph edge goes to linkage
// with no node in the collector's own graph, while an own-graph proxy node goes
// to the collector's own graph and never touches linkage. An author who reached
// for the wrong one would get a graph that looks empty in the place they looked.
// Neither statement has a gate other than this one.
//
// IT READS THE PAGE THROUGH THIS PACKAGE'S docs SYMLINK, which is what puts the
// guide in this module's test-cache key; see collect_docs_contract_test.go for
// why a walked-up relative path would be dropped from the key and served a
// stored pass on a docs-only edit.

// collectorGuide reads the custom-collector guide through the fenced link.
func collectorGuide(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(docsLinkDir, "tools", "custom_collector.md"))
	require.NoError(t, err, "the collector guide must be readable through %s", docsLinkDir)
	require.NotEmpty(t, body)
	return string(body)
}

// TestCollectorGuide_DocumentsTheTargetGraphField asserts the field is in the
// edge-fields table and in the guide's restatement of the output schema, so an
// author reading either surface finds it.
func TestCollectorGuide_DocumentsTheTargetGraphField(t *testing.T) {
	guide := collectorGuide(t)

	require.Contains(t, guide, "`target_graph`",
		"the edge-fields table must carry the field: the guide states that a field not in its tables is an error")

	// It is in the TABLE specifically, not merely somewhere on the page. The
	// table rows are the pipe-delimited lines, and the field's row is the one
	// that starts with it.
	inTable := false
	for line := range strings.SplitSeq(guide, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "| `target_graph` |") {
			inTable = true
			assert.Contains(t, line, "Optional",
				"the row must say the field is optional; an author reading it as required would set it on every edge")
			break
		}
	}
	assert.True(t, inTable, "target_graph must be a ROW of the edge-fields table, not only prose elsewhere on the page")

	assert.Contains(t, guide, `"target_graph": { "type": "string" }`,
		"the guide's restatement of the output schema must declare the property, or it disagrees with the checked-in schema it claims to restate")
}

// TestCollectorGuide_DocumentsTheSourceGraphField is the same gate for the
// mirror field. An author who finds only target_graph reads the carrier as
// one-directional, and a relationship whose foreign endpoint is the SOURCE then
// gets reversed or emitted with no family at all — the two wrong forcings the
// Kubernetes collector held its Helm relationship back rather than take.
func TestCollectorGuide_DocumentsTheSourceGraphField(t *testing.T) {
	guide := collectorGuide(t)

	require.Contains(t, guide, "`source_graph`",
		"the edge-fields table must carry the field: the guide states that a field not in its tables is an error")

	inTable := false
	for line := range strings.SplitSeq(guide, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "| `source_graph` |") {
			inTable = true
			assert.Contains(t, line, "Optional",
				"the row must say the field is optional; an author reading it as required would set it on every edge")
			break
		}
	}
	assert.True(t, inTable, "source_graph must be a ROW of the edge-fields table, not only prose elsewhere on the page")

	assert.Contains(t, guide, `"source_graph": { "type": "string" }`,
		"the guide's restatement of the output schema must declare the property, or it disagrees with the checked-in schema it claims to restate")
}

// TestCollectorGuide_StatesTheBothFieldsRefusalAsACollectTimeOne is the
// refusal's own row, and WHEN it fires is as load-bearing as that it fires. It
// is refused during the collect's resolution pass, not at registration: an
// author told otherwise ships a collector that registers cleanly and then fails
// every collect. This guide has already shipped that mistake once, promising a
// registration-time refusal that nothing performs.
func TestCollectorGuide_StatesTheBothFieldsRefusalAsACollectTimeOne(t *testing.T) {
	guide := collectorGuide(t)

	assert.Contains(t, guide, "names both `source_graph` and `target_graph`",
		"the guide must state the refusal in the author's own terms: the two fields on one edge")
	assert.Contains(t, guide, "Four conditions fail the whole collect",
		"and count it among the collect-time refusals, which is where it fires")
	assert.NotContains(t, guide, "Three conditions fail the whole collect",
		"the retired count must not survive beside the corrected one; a partial edit leaves exactly that")
	assert.NotContains(t, guide, "refused at registration when it names both",
		"the refusal is a collect-time one; nothing on the registration path reads an edge")
}

// TestCollectorGuide_StatesWhichShapeUsesWhichMechanism is the mechanism
// statement R4 owes the reader: two shapes, two graphs, and when to use each.
func TestCollectorGuide_StatesWhichShapeUsesWhichMechanism(t *testing.T) {
	guide := collectorGuide(t)

	for _, want := range []struct {
		phrase string
		why    string
	}{
		{"not written into your own graph",
			"a cross-graph edge does not land in the collector's own graph, and an author who assumed it did would look for it in the wrong place"},
		{"linkage graph",
			"the guide must name the graph a cross-graph edge actually lands in"},
		{"needs no field",
			"the own-graph proxy shape uses the plain contract; an author told otherwise would reach for target_graph and get the wrong graph"},
		{"family",
			"target_graph names a graph FAMILY rather than one graph instance"},
	} {
		assert.Contains(t, guide, want.phrase, want.why)
	}

	// THE THREE REFUSALS ARE DOCUMENTED, because each one fails the whole
	// collect. A collector author who does not know that will read a failed
	// collect as a bug in the client.
	for _, want := range []string{
		"no graph of the named family is loaded",
		"is in none of that family's graphs",
		"could not be enumerated",
	} {
		assert.Contains(t, guide, want,
			"the guide must name the condition; each one fails the whole collect rather than warning")
	}
}

// TestCollectorGuide_CarriesNoInternalReferences is the shipped-surface guard on
// the text this change added. The guide is published OSS, so it may name no
// tracker id, no node id and no private backend host.
func TestCollectorGuide_CarriesNoInternalReferences(t *testing.T) {
	guide := collectorGuide(t)
	for _, forbidden := range []string{"FUL-", "fulminate.io", "linear.app"} {
		assert.NotContains(t, guide, forbidden,
			"the collector guide ships to the public OSS mirror and may not carry %q", forbidden)
	}
}
