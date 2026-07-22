// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doctorDeps is a healthy, logged-OUT local deps that ALSO implements the
// optional doctorChecker seam, so handleServerStatus's local json + text
// branches emit the doctor block. Mirrors the healthDeps shape in
// manage_status_test.go (embed cloudStatusDeps for the full ClientDeps surface,
// override LocalLiveness with a fake, add the one optional method).
type doctorDeps struct {
	*cloudStatusDeps
	live   LocalLiveness
	checks []DoctorCheck
}

func (d *doctorDeps) LocalLiveness() LocalLiveness                 { return d.live }
func (d *doctorDeps) DoctorChecks(_ context.Context) []DoctorCheck { return d.checks }

// TestHandleServerStatus_DoctorBlock is the step criterion: a deps implementing
// doctorChecker yields a non-empty doctor[] whose entries carry
// name/status(pass|warn|fail|info)/detail/remediation in both the json map and
// the compact text block; a deps NOT implementing it omits the key entirely (the
// additive degrade contract).
func TestHandleServerStatus_DoctorBlock(t *testing.T) {
	live := fakeLiveness{status: runningStatusMap()}
	checks := []DoctorCheck{
		{Name: "config", Status: "pass", Detail: "~/.knowledge/config valid", Remediation: ""},
		{Name: "voyage", Status: "info", Detail: "VOYAGE_API_KEY unset — BM25-only", Remediation: ""},
		{Name: "summarizer-cli", Status: "fail", Detail: "cli_bin not set in config", Remediation: "add cli_bin = \"/path\" to [default]"},
	}

	t.Run("emits doctor when implemented", func(t *testing.T) {
		deps := &doctorDeps{cloudStatusDeps: &cloudStatusDeps{loggedIn: false}, live: live, checks: checks}

		var got struct {
			Doctor []DoctorCheck `json:"doctor"`
		}
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(deps, "json"))), &got))
		require.NotEmpty(t, got.Doctor, "doctor[] must be present and non-empty when doctorChecker is implemented")
		allowed := map[string]bool{"pass": true, "warn": true, "fail": true, "info": true}
		for i, c := range got.Doctor {
			assert.NotEmpty(t, c.Name, "check %d carries a name", i)
			assert.True(t, allowed[c.Status], "check %d status %q must be pass|warn|fail|info", i, c.Status)
		}
		// The fail entry round-trips detail + remediation.
		last := got.Doctor[len(got.Doctor)-1]
		assert.Equal(t, "fail", last.Status)
		assert.Equal(t, "cli_bin not set in config", last.Detail)
		assert.Contains(t, last.Remediation, "add cli_bin")

		// The human text render carries the compact "what's broken" block.
		body := textBodyTools(handleServerStatus(deps, ""))
		assert.Contains(t, body, "Doctor (what's broken):")
		assert.Contains(t, body, "cli_bin not set in config")
	})

	t.Run("omits doctor when not implemented", func(t *testing.T) {
		deps := &localNoHealthDeps{cloudStatusDeps: &cloudStatusDeps{loggedIn: false}, live: live}

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(deps, "json"))), &got))
		assert.NotContains(t, got, "doctor", "a deps not implementing doctorChecker omits the doctor key")

		body := textBodyTools(handleServerStatus(deps, ""))
		assert.NotContains(t, body, "Doctor (what's broken):")
	})
}
