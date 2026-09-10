// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// graph_vocabulary_test.go — R6: the `graph` param of search and traverse must
// advertise registered custom graph types, matching query and manage.
//
// THE EXPECTATION IS READ FROM query AND manage RATHER THAN HARDCODED, so the
// four surfaces stay consistent as the wording evolves: a test carrying its own
// expected string would keep passing while the vocabulary drifted apart.

// customGraphPhrase is the substring query and manage already use to admit a
// registered custom graph type into a graph-selector vocabulary. It is derived
// from those two definitions rather than asserted about them.
const customGraphPhrase = "registered custom graph type"

func TestQueryAndManageAlreadyAdvertiseCustomGraphs(t *testing.T) {
	q := QueryToolDef().InputSchema.Properties["graph"].Description
	require.Contains(t, q, customGraphPhrase,
		"query's graph param is the wording the other surfaces are matched against")
	require.Contains(t, ManageToolDef().Description, customGraphPhrase,
		"manage's description is the second reference wording")
}

// TestSearchGraphParamAdvertisesCustomGraphs pins search's TWO sites: the tool
// Description (which enumerates the routable graphs) and the graph property.
func TestSearchGraphParamAdvertisesCustomGraphs(t *testing.T) {
	def := SearchToolDef()
	require.Contains(t, strings.ToLower(def.Description), customGraphPhrase,
		"search's tool Description enumerates the routable graphs and must name registered custom graph types")
	require.Contains(t, def.InputSchema.Properties["graph"].Description, customGraphPhrase,
		"search's graph param must name registered custom graph types")
}

// TestTraverseGraphParamAdvertisesCustomGraphs pins traverse's ONE site. Its
// tool Description enumerates no graph family at all — it names only the
// graph-selector fields — so the graph property is traverse's whole R6 edit.
func TestTraverseGraphParamAdvertisesCustomGraphs(t *testing.T) {
	def := TraverseToolDef()
	require.Contains(t, def.InputSchema.Properties["graph"].Description, customGraphPhrase,
		"traverse's graph param must name registered custom graph types")
}
