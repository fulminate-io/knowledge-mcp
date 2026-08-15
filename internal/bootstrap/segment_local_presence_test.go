// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestSegmentBearingGraphs_SkipsAbsentCodeRepo pins the second membership
// condition: interaction admits a graph, but for a CODE graph this client also
// has to hold the codebase before it does any segment work on it. A client
// without the checkout cannot read what it would be probing, healing or
// publishing; the machine that does hold the repo is the one that publishes.
//
// The dropped member is asserted against TWO survivors in the same run, and both
// are load-bearing. The present code graph proves the gate discriminates on
// presence rather than simply excluding the code family; the non-code graph
// proves it discriminates on family rather than dropping whatever is absent from
// a repo manifest that has nothing to say about a cloud account. Without them a
// gate stuck at false would satisfy the assertion just as well.
func TestSegmentBearingGraphs_SkipsAbsentCodeRepo(t *testing.T) {
	c, _ := buildReconcileClient(t)

	const (
		presentRepo = "repo-this-machine-has"
		absentRepo  = "repo-this-machine-lacks"
		cloudAcct   = "some-cloud-account"
	)

	// Presence is stated directly rather than by planting a manifest: the subject
	// here is what segmentBearingGraphs DOES with the answer. That the real
	// predicate computes the answer correctly from the manifest plus the disk is
	// TestLocalCodeRepoPresent_ManifestAndDir's subject, and that production wires
	// the real predicate rather than a stub is pinned by the wiring criteria.
	c.localPresence = func(gt kgtypes.GraphType, name string) bool {
		return gt != kgtypes.GraphCode || name == presentRepo
	}

	c.workingSet = workingset.New()
	c.AdmitGraph(kgtypes.GraphCode, presentRepo, "search")
	c.AdmitGraph(kgtypes.GraphCode, absentRepo, "search")
	c.AdmitGraph(kgtypes.GraphCloud, cloudAcct, "search")

	// All three were admitted — the drop below is the presence gate acting, not an
	// admission that never happened.
	require.Len(t, c.workingSet.Members(), 3,
		"precondition: every graph under test is in the working set")

	got := c.segmentBearingGraphs()

	require.ElementsMatch(t,
		[]segmentGraphRef{
			{gt: kgtypes.GraphCode, name: presentRepo},
			{gt: kgtypes.GraphCloud, name: cloudAcct},
		},
		got,
		"an admitted code graph with no local checkout is dropped, while the code graph WITH a "+
			"checkout and the non-code graph both survive")
}
