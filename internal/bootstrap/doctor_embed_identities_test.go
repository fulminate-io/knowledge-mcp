// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestDoctorEmbedIdentities_Branches pins the three outcomes of the config half,
// including the one a careless implementation collapses: "nothing embedded yet"
// reported as "everything is fine".
func TestDoctorEmbedIdentities_Branches(t *testing.T) {
	cfg, err := config.Parse([]byte(""))
	require.NoError(t, err)

	t.Run("an unconstructible identity is an ERROR naming the graphs", func(t *testing.T) {
		t.Setenv("VOYAGE_API_KEY", "")
		t.Setenv("HOME", t.TempDir())

		res, failed := assembleIdentityCheck(cfg, []config.LiveGraphIdentity{{
			GraphType: "code", Name: "alpha",
			Identity: config.RecordedIdentity{
				Provider: config.EmbedProviderVoyage, Model: "voyage-code-3", Dimension: 1024, Dtype: "ubinary",
			},
		}})
		require.True(t, failed, "the caller must stop here rather than continue to the ok render")
		assert.Equal(t, statusErr, res.status,
			"a graph whose vectors cannot be searched semantically is an ERROR, not a warning — "+
				"the alternative is a search that quietly returns BM25 results while reporting success")
		assert.Contains(t, res.detail, "code/alpha", "and the detail names the dependent graph")
		assert.Equal(t, "embed-identities", res.name)
	})

	t.Run("no identity recorded is INFO, not ok", func(t *testing.T) {
		res := assembleOKResult([]config.LiveGraphIdentity{{GraphType: "code", Name: "alpha"}})
		assert.Equal(t, statusInfo, res.status,
			"a corpus that has never embedded has not been shown to be fine; reporting ok would be "+
				"the silent pass this check exists to prevent")
	})

	t.Run("a constructible identity is ok and counts the embedded graphs", func(t *testing.T) {
		res := assembleOKResult([]config.LiveGraphIdentity{
			{GraphType: "code", Name: "alpha", Identity: config.RecordedIdentity{
				Provider: config.EmbedProviderFake, Model: "canned", Dimension: 256, Dtype: "ubinary",
			}},
			{GraphType: "code", Name: "beta"},
		})
		assert.Equal(t, statusOK, res.status)
		assert.Contains(t, res.msg, "1 of 2", "the count distinguishes embedded graphs from all graphs")
	})
}

// TestDoctorEmbedIdentities_IsWiredIn is the wiring leg, and it is separate from
// the branch legs on purpose: every assertion above passes against a check that
// is written, correct, and never run.
//
// defaultChecks IS THE ONLY PLACE a check becomes reachable — it feeds both the
// `knowledge doctor` CLI and the MCP manage(status) doctor block — so this
// asserts membership there rather than in either consumer.
func TestDoctorEmbedIdentities_IsWiredIn(t *testing.T) {
	// A port nothing is listening on: the liveness probe fails, the check returns
	// its no-server INFO, and it is still present in the slice. That is what is
	// being asserted — presence, not outcome — so the test needs no live server.
	checks := defaultChecks(1, t.TempDir()+"/no-such-config")

	var found *checkResult
	for i := range checks {
		if checks[i].name == "embed-identities" {
			found = &checks[i]
		}
	}
	require.NotNil(t, found,
		"the embed-identity check must be in defaultChecks, or it is written and never runs")

	// KNOWN-POSITIVE, same run: a check that IS wired today is found by the same
	// walk, so a failure above is about this check rather than about the walk.
	var control bool
	for _, c := range checks {
		if c.name == "config" {
			control = true
		}
	}
	require.True(t, control, "control: the walk finds a check known to be wired")
}
