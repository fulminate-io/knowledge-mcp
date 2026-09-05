// SPDX-License-Identifier: Apache-2.0

package collector_test

// collect_gate_identity_families_test.go pins the collect gate's RECORDED
// IDENTITY against the name each collector family actually produces, one family
// per subtest.
//
// NEITHER SIDE OF ANY EQUALITY HERE IS A STRING THIS FILE WRITES. The predicted
// side is tools.CollectGateGraphName, the single production derivation the
// dispatch calls; the expected side is the provider package's own GraphName —
// the very function that provider's collector fills CollectResult.GraphName
// from — or, for the cloud families, the collect id the collector passes
// through. A test that hardcoded an expected string would be comparing its own
// answer key against itself and would stay green while the gate went inert.
//
// THE SUBTEST NAMES ARE PART OF THE CONTRACT. The plan's identity criterion
// greps for each family's `--- PASS: <parent>/<name>` line by name, so renaming
// a subtest silently un-gates that family. Do not rename one without changing
// the criterion.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/bitbucket"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/github"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/gitlab"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// familiesTestOrgID is the org/workspace id every cicd case shares, so the three
// providers are compared on IDENTICAL input — which is what makes the
// cross-wiring control below meaningful.
const familiesTestOrgID = "fulminate-io"

// TestCollectGateGraphName_NamesEveryCICDProvider proves the collect dispatch
// derives, for each cicd provider, exactly the graph name that provider's
// collector emits — so a collect of one holds the gap-scan gate over its own
// graph instead of gating nothing.
func TestCollectGateGraphName_NamesEveryCICDProvider(t *testing.T) {
	cases := []struct {
		name string // LOCKED — the criterion greps for this subtest by name.
		want string
	}{
		{"github", github.GraphName(familiesTestOrgID)},
		{"gitlab", gitlab.GraphName(familiesTestOrgID)},
		{"bitbucket", bitbucket.GraphName(familiesTestOrgID)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tools.CollectGateGraphName(tc.name, familiesTestOrgID, nil)
			require.NoError(t, err, "the %s derivation must not refuse a plain org id", tc.name)
			// AHEAD OF THE EQUALITY, so a red reads as "no arm" rather than as a
			// value mismatch: an arm the derivation is missing returns the empty
			// string, records no gate identity, and leaves the gate inert for the
			// whole family.
			require.NotEmpty(t, got,
				"the %s arm derives nothing, so a %s collect records no gate identity "+
					"and the gap-scan gate stays inert for the entire family", tc.name, tc.name)
			require.Equal(t, tc.want, got,
				"the recorded identity must equal the name the %s collector emits — "+
					"a predicted name matching no collector's name gates nothing", tc.name)
		})
	}

	// CROSS-WIRING CONTROL. Three providers handed ONE id must derive three
	// DIFFERENT names. NAME THE CATCHER: this is the only assertion that fails
	// when an arm is copy-pasted onto the wrong provider's derivation — a
	// one-word edit that compiles and satisfies every NotEmpty leg above.
	gh := github.GraphName(familiesTestOrgID)
	gl := gitlab.GraphName(familiesTestOrgID)
	bb := bitbucket.GraphName(familiesTestOrgID)
	require.NotEqual(t, gh, gl, "github and gitlab must not derive the same graph name for one id")
	require.NotEqual(t, gh, bb, "github and bitbucket must not derive the same graph name for one id")
	require.NotEqual(t, gl, bb, "gitlab and bitbucket must not derive the same graph name for one id")
}

// familiesTestAccountID is the account/project/context id every cloud leg shares.
const familiesTestAccountID = "prod-account-4711"

// TestCollectGateGraphName_NamesTheCloudCollectorsThatPassTheIDThrough proves the
// dispatch derives, for each cloud collector that names its graph after the id it
// was handed, exactly that id — and that aws, which cannot be predicted from the
// request, keeps deriving nothing.
func TestCollectGateGraphName_NamesTheCloudCollectorsThatPassTheIDThrough(t *testing.T) {
	// LOCKED subtest names — the criterion greps for each one.
	for _, ct := range []string{"gcp", "azure", "k8s"} {
		t.Run(ct, func(t *testing.T) {
			got, err := tools.CollectGateGraphName(ct, familiesTestAccountID, nil)
			require.NoError(t, err, "the %s derivation must not refuse a plain id", ct)
			require.Equal(t, familiesTestAccountID, got,
				"the %s collector names its graph after the id it was handed, so the "+
					"derivation must be that id VERBATIM — anything else records a gate "+
					"identity no registered collector can match", ct)
		})
	}

	// aws — the documented ABSENCE, carried independently of the landed scope
	// control in collect_gate_identity_test.go so a later reader cannot quietly
	// add an arm the collector would not honor.
	t.Run("aws", func(t *testing.T) {
		got, err := tools.CollectGateGraphName("aws", "123456789012", nil)
		require.NoError(t, err, "an aws collect must not be refused by the derivation")
		require.Empty(t, got,
			"the aws collector DISCARDS the collect id and names its graph from the STS "+
				"caller identity read during the walk, so any name derived from the request "+
				"would hold the gap-scan gate over a graph nobody is collecting while the "+
				"real account graph scanned unguarded")
	})

	// SCOPE CONTROL, deliberately outside the subtests so it is not attributed to
	// any one family. NAME THE CATCHER: this is what fails if the switch is
	// widened by a default that starts naming graphs for collectors that have none.
	for _, ct := range []string{"logs", "not-a-registered-collector-type"} {
		got, err := tools.CollectGateGraphName(ct, familiesTestAccountID, nil)
		require.NoError(t, err, "%s must not error", ct)
		require.Empty(t, got, "%s must derive no graph name", ct)
	}
}
