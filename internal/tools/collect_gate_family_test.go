// SPDX-License-Identifier: Apache-2.0

package tools

// collect_gate_family_test.go is the epoch half of the collect-gate family
// regression proof: a completed collect into a NON-CODE family must advance that
// family's own epoch, and must leave a same-named graph in another family alone.
//
// The identity is derived through the production derivation rather than written
// here, for the same reason the gate's own identity test does it: a hand-written
// name would put both sides of the equality under this file's control.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCollectEpoch_AdvancesForANonCodeCollect proves the collect epoch is keyed
// on the (family, name) PAIR: a finished pdf collect advances the pdf graph's
// epoch, and a code graph carrying the same name still reads 0.
func TestCollectEpoch_AdvancesForANonCodeCollect(t *testing.T) {
	pdfPath, err := filepath.Abs("../collector/pdf/testdata/form_xobject.pdf")
	require.NoError(t, err)
	graphName, err := CollectGateGraphName("pdf", pdfPath, nil)
	require.NoError(t, err, "the pdf derivation must not refuse an absolute path")
	require.NotEmpty(t, graphName, "a pdf collect must derive an epoch identity")

	rt := NewCollectRuntime()
	t.Cleanup(func() { rt.Stop(2 * time.Second) })

	// THE CONTROL FOR THE ZERO BELOW. Without a demonstrated 0-then-1 on the same
	// accessor, an epoch that never moved and one that was never wired look alike.
	require.Equal(t, uint64(0), rt.CompletedCollectsForGraph(kgtypes.GraphPDFRaw, graphName),
		"no collect has finished yet, so the pdf epoch must read 0")

	h, started, _ := rt.Start("pdf\x00"+pdfPath, "pdf "+pdfPath, kgtypes.GraphPDFRaw, graphName,
		func() (string, string, error) { return "", "", nil })
	require.True(t, started)
	<-h.Done()

	require.Equal(t, uint64(1), rt.CompletedCollectsForGraph(kgtypes.GraphPDFRaw, graphName),
		"a completed pdf collect must advance the pdf graph's own epoch — an epoch "+
			"pinned at zero leaves every stamped observation permanently fresh")

	// CROSS-FAMILY CONTROL for the epoch map's key. NAME THE CATCHER: this is what
	// fails if the completed map is keyed on the bare name, which would let a pdf
	// collect expire a code graph's observations.
	require.Equal(t, uint64(0), rt.CompletedCollectsForGraph(kgtypes.GraphCode, graphName),
		"a pdf collect must not advance the epoch of a CODE graph that shares its name")
}
