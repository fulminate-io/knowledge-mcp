// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestLocalPresenceSkip_LogsOncePerGraph pins the skip's observability AND its
// edge-triggering, which pull in opposite directions: a silent skip cannot be
// told from a broken gate, but the predicate runs on every reconcile tick and
// every catalog pass, so a line per call would be a metronome of noise on
// exactly the machines the gate helps.
//
// The three assertions are one test because each is the others' control. The
// repeated-call count proves the latch; the SECOND absent graph proves the latch
// is per-graph rather than a single global "already logged once" flag; and the
// present graph proves the line reports a decision rather than being emitted
// unconditionally by anything that consults the predicate.
func TestLocalPresenceSkip_LogsOncePerGraph(t *testing.T) {
	const (
		absentA     = "repo-absent-a"
		absentB     = "repo-absent-b"
		presentRepo = "repo-present"
		cloudAcct   = "cloud-account"
	)
	const skipMsg = "code graph skipped for background work"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	c := &client{
		localPresence: func(gt kgtypes.GraphType, name string) bool {
			return gt != kgtypes.GraphCode || name == presentRepo
		},
	}

	// Consult each of them repeatedly, as the reconcile and catalog passes do.
	for range 5 {
		require.False(t, c.graphLocallyPresent(kgtypes.GraphCode, absentA))
		require.False(t, c.graphLocallyPresent(kgtypes.GraphCode, absentB))
		require.True(t, c.graphLocallyPresent(kgtypes.GraphCode, presentRepo))
		require.True(t, c.graphLocallyPresent(kgtypes.GraphCloud, cloudAcct))
	}

	logged := buf.String()

	assert.Equal(t, 1, strings.Count(logged, "graph="+absentA),
		"the skip is edge-triggered: five consultations of the same absent graph log ONCE, "+
			"not once per reconcile tick")
	assert.Equal(t, 1, strings.Count(logged, "graph="+absentB),
		"the latch is per-graph, not a single global already-logged flag: a SECOND absent "+
			"graph gets its own line")
	assert.Equal(t, 2, strings.Count(logged, skipMsg),
		"exactly the two absent graphs are reported")

	// CONTROLS: neither a present code graph nor a non-code graph is a skip, so
	// neither may appear. Without these the counts above would also be satisfied
	// by a logger that fired for everything and happened to be latched.
	assert.NotContains(t, logged, "graph="+presentRepo,
		"a code graph WITH a local checkout is not skipped and must not be reported as one")
	assert.NotContains(t, logged, "graph="+cloudAcct,
		"a non-code graph is not gated at all — reporting it would claim a decision never made")
}
