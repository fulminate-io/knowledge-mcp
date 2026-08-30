// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncParityFixture is the shared active-time fixture, DECLARED ONCE in the upload-time
// rollup producer's testdata and read from here by relative path. Go runs a test with its
// package directory as the working directory, so the path resolves (the same idiom the
// collector's cross-package corpus test uses).
const syncParityFixture = "../transcriptsync/testdata/active_parity_instants.json"

// TestActiveMsFromInstants_SyncParityFixture is the daemon half of a two-sided parity
// gate: transcriptsync.rollupActiveMs is a hand-mirror of activeMsFromInstants, and
// TestRollupActiveMs_DaemonParityFixture over there runs the SAME fixture through the
// mirror. Two implementations asserting one declared-once set of expectations is what
// makes it a parity gate — two copies of the instants would let either side drift with
// its own implementation and stay green while the numbers diverged. Deleting one side
// leaves the other naming it.
func TestActiveMsFromInstants_SyncParityFixture(t *testing.T) {
	raw, err := os.ReadFile(syncParityFixture)
	require.NoError(t, err, "the shared parity fixture must be readable from this package")

	var fx struct {
		Cases []struct {
			Name             string   `json:"name"`
			Instants         []string `json:"instants"`
			ExpectedActiveMs int64    `json:"expected_active_ms"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &fx))
	require.NotEmpty(t, fx.Cases, "the fixture must carry cases")

	for _, c := range fx.Cases {
		instants := make([]time.Time, 0, len(c.Instants))
		for _, s := range c.Instants {
			ts, err := time.Parse(time.RFC3339Nano, s)
			require.NoError(t, err, "case %s instant %q", c.Name, s)
			instants = append(instants, ts)
		}
		assert.Equal(t, c.ExpectedActiveMs, activeMsFromInstants(instants), "case %s", c.Name)
	}
}
