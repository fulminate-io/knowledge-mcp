// SPDX-License-Identifier: Apache-2.0

package linker

// dockerfile_sweep_breadth_test.go — the SWEEP half of the Dockerfile pass's
// breadth contract: the all-graphs entry point itself, and the production call
// site that must keep reaching it.
//
// WHY IT IS A SECOND FILE. It is the same subject split by ARM, which is how the
// sibling's header already frames the pair: dockerfile_scope_test.go holds the
// SCOPED entry point's rows and this one holds the SWEEP's. They share one
// package, so scopeFake and its helpers are declared once next door and used
// here unchanged — the split is a file-length concern only, and no assertion or
// test name moved with it.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLinkDockerfiles_SweepsEveryGraphAndSkipsOverlays is ARM 2: the MANUAL
// operation keeps its breadth. It exists so the bound cannot be applied to both
// callers by accident — a change that scoped manage(operation:"link") too would
// pass every row of ARM 1 and fail here.
func TestLinkDockerfiles_SweepsEveryGraphAndSkipsOverlays(t *testing.T) {
	gc := newScopeFake(t, "repo-a", "repo-b", "repo-c")
	gc.overlayNames = []string{"repo-a@feature-branch"}

	n, err := LinkDockerfiles(context.Background(), gc, LinkOptions{})
	require.NoError(t, err)

	assert.Equal(t, 3, n, "the sweep links every non-overlay code graph, not just one")
	require.NotEmpty(t, gc.seq)
	assert.Equal(t, "enum", gc.seq[0],
		"the sweep OPENS with the code-family enumeration — that is where its breadth comes from")
	assert.Equal(t, 1, countSeq(gc.seq[:firstRead(gc.seq)+1], "enum"),
		"and it enumerates for breadth exactly once, before any node read")
	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		assert.Positivef(t, gc.readsByRepo[name], "the sweep must read %q", name)
	}
	assert.Zero(t, gc.readsByRepo["repo-a@feature-branch"],
		"an @-overlay name is skipped by the sweep as well")

	// AND ONE EDGE PER REPO, identified by the Dockerfile NODE ID the edge names
	// rather than by the proxy's repo segment: the proxy id's second segment comes
	// from crossgraph's own endpoint locate, which is a different mechanism and not
	// this test's subject.
	var froms []string
	for _, l := range gc.capturedLinks {
		froms = append(froms, l.FromID)
	}
	require.Len(t, froms, 3, "one BUILDS edge per non-overlay code graph")
	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		assert.Truef(t, containsSuffix(froms, name+":Dockerfile"),
			"the sweep emitted no edge for %q", name)
	}
}

// TestRunAll_CallsTheSweepNotTheScopedEntryPoint is ARM 2's breadth at its
// PRODUCTION CALL SITE, which is a different claim from the sweep unit's own.
//
// WHY THE UNIT TEST ABOVE DOES NOT COVER THIS. TestLinkDockerfiles_Sweeps...
// proves LinkDockerfiles enumerates; it says nothing about which entry point
// RunAll reaches for. Scoping client.go's call to one hardcoded graph — losing
// manage(operation:"link")'s entire value — leaves that test green, because the
// function it tests was not the one that changed. The subject here is the CALL.
//
// THE OBSERVABLE IS THE SAME scopeFake, so this row and its sibling in the tools
// package are one instrument read two ways: the sweep OPENS with a code-family
// enumeration, and the scoped tail opens with a node read of the graph it was
// named. Each is the other's control.
func TestRunAll_CallsTheSweepNotTheScopedEntryPoint(t *testing.T) {
	gc := newScopeFake(t, "repo-a", "repo-b", "repo-c")

	res, err := RunAll(context.Background(), gc, LinkOptions{})
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, 3, res.DockerfileLinks,
		"manage(operation:\"link\") links every non-overlay code graph; a scoped call here would link one")
	require.NotEmpty(t, gc.seq, "control: the pass issued code reads at all")
	assert.Equal(t, "enum", gc.seq[0],
		"RunAll must reach the SWEEP, which opens with the code-family enumeration — a scoped call "+
			"opens with a node read and this row is what tells the two apart at the call site")
	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		assert.Positivef(t, gc.readsByRepo[name], "the manual operation must read %q", name)
	}
}
