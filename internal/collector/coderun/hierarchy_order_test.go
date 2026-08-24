// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestHierarchyFromNodes_OrderIsDeterministic asserts the hierarchy builder's
// output ORDER is a function of its input, not of Go's map iteration.
//
// THIS TEST IS THE ONLY CATCHER. Remove either sort in HierarchyFromNodes and
// nothing else in the tree goes red: there is no build error, no lint error, and
// no other test reads the ORDER of the returned slices — they assert membership.
// The randomization is per-run, so a single call cannot see it either; the
// repetition below is what makes the defect observable at all.
//
// WHY ORDER MATTERS WHEN THE BYTES DO NOT. These rows carry identical non-key
// bytes, so a reordering is NOT proven to move a manifest digest and must not be
// claimed to. What it does prove is that the collect payload is not a function
// of the tree — which is the invariant the convergence claim rests on, and the
// place the next instance of this defect class would hide.
func TestHierarchyFromNodes_OrderIsDeterministic(t *testing.T) {
	// A fixed node set spanning several directories at several depths, including
	// a gap (cmd/knowledge/internal has no file of its own) so the intermediate
	// parent-chain nodes are exercised too.
	paths := []string{
		"README.md",
		"cmd/knowledge/main.go",
		"cmd/knowledge/internal/collector/coderun/hierarchy.go",
		"cmd/knowledge/internal/collector/parser/batchedges.go",
		"cmd/knowledge-server/main.go",
		"docs/collect-bench.md",
		"scripts/collect-bench.sh",
		"gen/knowledge/v1/knowledge.pb.go",
	}
	nodes := make([]*knowledgev1.Node, 0, len(paths))
	for _, p := range paths {
		nodes = append(nodes, &knowledgev1.Node{Id: p, Type: string(kgtypes.NodeFile)})
	}

	const calls = 8
	digests := make(map[string]int, calls)
	var first string
	for i := range calls {
		pkgNodes, edges := HierarchyFromNodes(nodes)

		// The KNOWN-POSITIVE CONTROL: a builder that returned nothing would give
		// eight identical digests over an empty string and pass vacuously.
		require.NotEmpty(t, pkgNodes, "call %d produced no package nodes — the builder did nothing", i)
		require.NotEmpty(t, edges, "call %d produced no edges — the builder did nothing", i)

		d := hierarchyDigest(pkgNodes, edges)
		if i == 0 {
			first = d
		}
		digests[d]++
	}

	require.Len(t, digests, 1,
		"HierarchyFromNodes produced %d DISTINCT orderings across %d calls over ONE fixed node set "+
			"(first was %s). Go randomizes map iteration per run, so a map walk in the builder makes the "+
			"collect payload a function of the runtime rather than of the tree",
		len(digests), calls, first)
}

// hierarchyDigest reduces one call's output to a digest over the emitted ORDER —
// ids and edge triples in the sequence they were returned, never sorted here.
// Sorting in the digest would defeat the entire test.
func hierarchyDigest(pkgNodes []*knowledgev1.Node, edges []*knowledgev1.Edge) string {
	h := sha256.New()
	for _, n := range pkgNodes {
		fmt.Fprintf(h, "N\t%s\t%s\n", n.Id, n.Type)
	}
	for _, e := range edges {
		fmt.Fprintf(h, "E\t%s\t%s\t%s\n", e.FromId, e.ToId, e.Type)
	}
	return hex.EncodeToString(h.Sum(nil))
}
