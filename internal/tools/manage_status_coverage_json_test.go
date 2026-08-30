// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// manage_status_coverage_json_test.go — the format:json coverage[] WIRE CONTRACT:
// the ten pinned keys, and what an unmanaged row actually carries in them.
//
// SPLIT OUT OF manage_status_coverage_test.go, unchanged, when that file reached
// the repo's hard 500-line cap. The seam is the honest one: everything here is
// about the JSON a consumer types against, while the sibling is about the markdown
// table and the fakes both render through.

// TestCoverageRowJSONKeysUnchanged pins the WIRE CONTRACT the Daemon Status web
// Coverage card types against: exactly ten snake_case keys, no eleventh.
//
// It asserts SET EQUALITY rather than a count, deliberately — a count of ten is
// satisfied by dropping one key and adding another, which is precisely the shape a
// careless rename produces. The row is populated with non-zero values throughout so
// no key can be omitted by an accidental omitempty.
func TestCoverageRowJSONKeysUnchanged(t *testing.T) {
	row := CoverageRow{
		Graph: "code/knowledge", Total: 10, Summarized: 9, Embedded: 8,
		SegCovered: 7, LiveResident: 6, HasSegments: true,
		SummaryFail: 1, EmbedFail: 2, SegDisposition: DispositionCacheAged,
		// The new field must contribute NO key — it exists to feed the disposition,
		// and a json tag on it would ship an eleventh key to a ten-key consumer.
		RepairVerified: true,
		// SegProbed is the same shape and the same rule: it decides how the segment
		// CELL renders, and widening a pinned wire shape is a separate decision from
		// adding a column to the table.
		SegProbed: true,
	}

	raw, err := json.Marshal(row)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	got := make([]string, 0, len(decoded))
	for k := range decoded {
		got = append(got, k)
	}
	require.ElementsMatch(t, []string{
		"graph", "total", "summarized", "embedded", "seg_covered",
		"live_resident", "has_segments", "summary_fail", "embed_fail", "seg_disposition",
	}, got, "the ten pinned keys, and no eleventh")
	require.NotContains(t, decoded, "repair_verified",
		"the verified input is json:\"-\" — it must never reach the wire")
	require.NotContains(t, decoded, "seg_probed",
		"the probed input is json:\"-\" — it must never reach the wire")
}

// TestUnmanagedRowJSONCarriesRealCountsAndUnprobedZeros is the honest record of
// what the format:json coverage[] block ships for an unmanaged row, now that the
// counts are real.
//
// WHAT IMPROVED, and it is the ruling's point: total / summarized / embedded /
// summary_fail / embed_fail are the graph's OWN numbers on the wire, where they
// were four fabricated zeros before. A consumer rendering total for an unmanaged
// row now reads 366842 rather than 0.
//
// WHAT DID NOT, and this is asserted rather than glossed: seg_covered and
// live_resident are STILL zeros nobody measured, because the segment probes stay
// declined for an unmanaged graph and the ten-key wire shape carries no marker for
// an unprobed pool. has_segments is true beside them, so the pair reads exactly
// like the "shipped 0 · live 0" case has_segments was introduced to prevent. The
// ONLY discriminator on the wire is seg_disposition == "unmanaged", which does mean
// precisely "outside the working set, nothing services it" — so a consumer must
// branch on the band before rendering either segment figure. This test states that
// obligation in the one place a change to it would be noticed.
func TestUnmanagedRowJSONCarriesRealCountsAndUnprobedZeros(t *testing.T) {
	_, _, deps := unmanagedFixture()

	m := map[string]any{}
	addLocalDaemonJSON(context.Background(), deps, m)
	raw, err := json.Marshal(m["coverage"])
	require.NoError(t, err)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(raw, &rows))

	var unmanaged, managed map[string]any
	for _, r := range rows {
		switch r["graph"] {
		case "code/foreignrepo":
			unmanaged = r
		case "code/managedrepo":
			managed = r
		}
	}
	require.NotNil(t, unmanaged, "the unmanaged graph must appear in coverage[]")
	require.NotNil(t, managed, "control: the managed graph's row must be there too")

	assert.InDelta(t, 366842.0, unmanaged["total"], 0.5,
		"the count fields carry the graph's real numbers, not the zeros they used to")
	assert.InDelta(t, 366842.0, unmanaged["summarized"], 0.5)
	assert.InDelta(t, 366842.0, unmanaged["embedded"], 0.5)

	assert.Equal(t, DispositionUnmanaged, unmanaged["seg_disposition"],
		"the band is the ONLY wire discriminator for the two segment figures below")
	assert.Equal(t, true, unmanaged["has_segments"])
	assert.InDelta(t, 0.0, unmanaged["seg_covered"], 0.5,
		"UNMEASURED, not measured-zero: the segment probe is declined for an unmanaged graph "+
			"and the ten-key shape carries no marker for it — a consumer must branch on the band")
	assert.InDelta(t, 0.0, unmanaged["live_resident"], 0.5,
		"same: unprobed, not zero")

	// The control proves those zeros are the DECLINED PROBE rather than a fixture
	// that programmed no pool: the managed row's identical fields carry real figures.
	assert.InDelta(t, 800.0, managed["seg_covered"], 0.5,
		"control: a probed row's segment figures are populated from the same fixture")
	assert.InDelta(t, 800.0, managed["live_resident"], 0.5)
}
