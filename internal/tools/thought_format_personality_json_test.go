// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// personalityJSONFixture builds eight clusters whose row defaults FAN across the
// scalar range, with no deviations. The fan is what makes the payload control
// below discriminating: the rendered rows are drawn from the extreme rows at each
// end, so at least one middle cluster is guaranteed to stay out of them.
func personalityJSONFixture() ([]clientthought.ThoughtCluster, *clientthought.PersonalityProfile) {
	defaults := []float64{0.10, 0.20, 0.30, 0.40, 1.00, 1.60, 1.70, 1.80}
	clusters := make([]clientthought.ThoughtCluster, 0, len(defaults))
	profile := &clientthought.PersonalityProfile{
		RowDefault:    make(map[string]float64, len(defaults)),
		ClusterLabels: make(map[string]string, len(defaults)),
	}
	for i, value := range defaults {
		id := fmt.Sprintf("jc%02d", i)
		label := fmt.Sprintf("JSONLABEL-%02d", i)
		clusters = append(clusters, clientthought.ThoughtCluster{ID: id, Label: label, Size: 1})
		profile.RowDefault[id] = value
		profile.ClusterLabels[id] = label
	}
	return clusters, profile
}

// TestPersonalityReport_JSONCarriesRenderedRowsOnly pins the wire surface of
// query(mode:"personality", format:"json"): the RENDERED rows and the cluster
// count, and nothing else.
//
// It drives the real handler rather than marshaling a struct directly, because
// the surface under test is what the handler emits — only that path exercises the
// defensive clone, the topic labeling and the JSON result together.
func TestPersonalityReport_JSONCarriesRenderedRowsOnly(t *testing.T) {
	clusters, profile := personalityJSONFixture()
	deps := interceptTestDeps{
		gc:              &reflectFakeCaller{}, // no topic docs → labels stay as seeded
		clusters:        clusters,
		clusterProfile:  profile,
		clusterComputed: true,
	}

	payload := resultText(handleReflectPersonality(context.Background(), deps, queryReflectArgs{Format: "json"}))

	// (1) SURFACE PIN. An exact key set rather than an absence grep for a retired
	// field name: this also catches a NEW field added to the report without anyone
	// deciding it belongs on the wire.
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(payload), &raw), "the json arm must emit a JSON object")
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	require.Equal(t, []string{"ClusterCount", "TopGullible", "TopStubborn"}, keys,
		"the personality JSON arm carries the rendered rows and the cluster count ONLY")

	// (2) KNOWN-POSITIVE CONTROL. Without this, an empty report satisfies the key
	// set above and the test proves nothing.
	var report clientthought.PersonalityReport
	require.NoError(t, json.Unmarshal([]byte(payload), &report))
	require.NotEmpty(t, report.TopStubborn, "control: the fixture must actually render rows")
	assert.Contains(t, payload, report.TopStubborn[0].LabelA,
		"control: a rendered row's label reaches the wire")

	// (3) PAYLOAD CONTROL, derived from the report rather than assumed. Collect every
	// cluster the rendered rows mention, then find a fixture cluster outside that set
	// and require it to be absent from the payload. Deriving it keeps the test correct
	// whatever the top-K selection picks.
	rendered := make(map[string]bool)
	for _, rows := range [][]clientthought.ClusterPairScalar{report.TopStubborn, report.TopGullible} {
		for _, row := range rows {
			rendered[row.ClusterA] = true
			rendered[row.ClusterB] = true
		}
	}
	unrenderedID := ""
	for _, cluster := range clusters {
		if !rendered[cluster.ID] {
			unrenderedID = cluster.ID
			break
		}
	}
	require.NotEmpty(t, unrenderedID,
		"control: the fixture must leave at least one cluster out of the rendered rows, or this assertion is vacuous")
	assert.NotContains(t, payload, unrenderedID,
		"a cluster outside the rendered rows must not reach the wire")
	assert.NotContains(t, payload, profile.ClusterLabels[unrenderedID],
		"an unrendered cluster's label must not reach the wire either")
}
