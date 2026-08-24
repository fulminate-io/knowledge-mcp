//go:build collectbench

// SPDX-License-Identifier: Apache-2.0

package bench

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectbench_residue_test.go is the gate for the NODE-ORPHAN RECLAIM: a node
// the client has stopped emitting must not survive on the server.
//
// WHY THIS GATE NEEDED A PURPOSE-BUILT CONTROL. The orphan population it was born
// from — five files carrying 1/3/1/1/1 excess server rows — was minted by a
// LINE-SHIFTING mutation, and it orphaned nodes only because ids were
// POSITION-DERIVED. Under content-derived ids a line shift moves no id at all, so
// that same mutation now leaves nothing to reclaim: excess-before and
// excess-after are both zero, the reclaim never fires, and a gate written against
// it passes having tested nothing. Quiescence and convergence also go green
// without the deletion basis ever running, so the whole completion bar could flip
// with this half untested.
//
// SO THE CONDUCTOR'S CONTROL CHANGES A NODE SET RATHER THAN LINE NUMBERS: it
// appends a uniquely-named top-level declaration, collects, then removes it and
// collects again. That is an uncarried orphan under EITHER id scheme.
//
// THE BEFORE-COUNT IS ASSERTED, NOT PRINTED. A zero there means the control
// declaration never produced a node, and a zero-after over a population that was
// never non-zero proves nothing — so this test refuses the run rather than
// reporting a green. A run in which the reclaim NO-OPS goes red on the after
// count. Those two assertions together are what make the gate falsifying.

// residueControl is the conductor's residue.json.
type residueControl struct {
	RunLabel               string `json:"run_label"`
	ControlFile            string `json:"control_file"`
	ControlDeclID          string `json:"control_decl_id"`
	LiveBefore             int    `json:"live_before"`
	LiveAfter              int    `json:"live_after"`
	PopulationOrphansAfter int    `json:"population_orphans_after"`
}

// TestCollectBench_NodeOrphanResidueIsZero asserts the reclaim removed the node
// the client stopped emitting, against a known-positive that proves the node was
// there to remove.
func TestCollectBench_NodeOrphanResidueIsZero(t *testing.T) {
	var c residueControl
	readJSON(t, "residue.json", &c)

	require.NotEmpty(t, c.ControlDeclID,
		"the conductor wrote no control declaration id — residue.json is malformed and "+
			"the assertions below would be about nothing")

	// THE KNOWN-POSITIVE, FIRST. Everything after it is meaningless without it.
	require.Positive(t, c.LiveBefore,
		"KNOWN-POSITIVE CONTROL: the control declaration %s produced NO live node before "+
			"its removal, so there was never an orphan to reclaim and a zero-after below "+
			"would prove nothing. Either the declaration was not collected or the control "+
			"file %s is not in the collected set",
		c.ControlDeclID, c.ControlFile)

	// THE PROPERTY. A reclaim that no-ops leaves this non-zero, which is exactly
	// the run that must go red.
	assert.Zero(t, c.LiveAfter,
		"the node-orphan reclaim did not fire: %s is still live on the server after the "+
			"declaration was removed from the tree and the tree re-collected (%d live rows). "+
			"The client stopped emitting it, so nothing but the reclaim can remove it",
		c.ControlDeclID, c.LiveAfter)

	// A SECOND, NARROWER MEASUREMENT, REPORTED BESIDE THE CONTROL — and its claim is
	// scoped to what it actually counts. It measures live nodes belonging to files
	// the collect no longer emits a FILE NODE for, which is a DIFFERENT question
	// from the one the control answers: the reclaim's own subject is an uncarried
	// node inside a file that is STILL PRESENT, and such a file always has a live
	// file node, so it can never appear in this count. Read this as a whole-file
	// disappearance check, never as coverage of the reclaim across the population —
	// the LiveBefore>0 / LiveAfter==0 pair above is the real gate, and it is the one
	// that carries a known-positive.
	assert.Zero(t, c.PopulationOrphansAfter,
		"%d live node(s) remain for files the collect no longer emits a file node for",
		c.PopulationOrphansAfter)

	t.Logf("residue control %s: live_before=%d live_after=%d population_orphans_after=%d",
		c.ControlDeclID, c.LiveBefore, c.LiveAfter, c.PopulationOrphansAfter)
}
