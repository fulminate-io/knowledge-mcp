// SPDX-License-Identifier: Apache-2.0

// IT IS AN EXTERNAL TEST PACKAGE ON PURPOSE. This is the one test here that
// takes a corpus root from the ENVIRONMENT, and an environment string is a
// taint source: read inside package parser it flows into Populate's own file
// walk and file reads, and the path-traversal analyzer reports at those
// production sinks rather than here. Everything this test needs is exported, so
// sitting outside the package removes the flow instead of annotating a
// production file for a test's benefit.
package parser_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestCDispatchPrecision measures what C's new dispatch capture actually
// produced on a real C corpus, against the zero-wrong-targets bar.
//
// THE FIXTURES IN THE EARLIER STEPS PROVE THE SHAPES ARE CAPTURED. They cannot
// prove the capture is PRECISE, because a fixture only contains what its author
// thought to write. This is the instrument that meets real source, and its
// printed strata are what a reviewer reads to see where the capture stops.
//
// IT SKIPS LOUDLY RATHER THAN PASSING QUIETLY. A run with no corpus root prints
// SKIP and does not report a PASS line, so the criterion that greps for one
// cannot be satisfied by an absent corpus.
func TestCDispatchPrecision(t *testing.T) {
	root := os.Getenv("F2_C_CORPUS_ROOT")
	if root == "" {
		t.Skip("F2_C_CORPUS_ROOT is unset: this measurement needs a real C corpus and reports nothing without one")
	}
	require.DirExistsf(t, root, "F2_C_CORPUS_ROOT names %q, which is not a directory", root)

	res, err := parser.Populate(context.Background(), "f2cprec", root)
	require.NoError(t, err)

	// THE DISPATCHED FIELD NAMES ARE READ FROM THE CHUNKER'S OWN CALLEE TEXT,
	// before resolution, so the wrong-target check below compares the resolver's
	// ANSWER against the source's QUESTION rather than against itself.
	dispatched, fieldNodes, files := cPrecisionCorpusFacts(t, root)

	var (
		callsFromC   int
		boundTyped   int
		dynamicEdges int
		byShape      = map[string]int{}
		wrongTargets []string
		slotBindEdge = map[string]int{}
		slotTargets  []string
	)
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeImplements {
			if shape, ok := strings.CutPrefix(e.Method, kgtypes.EdgeMethodSlotBind); ok {
				slotBindEdge[shape]++
				slotTargets = append(slotTargets, e.ToId)
			}
			continue
		}
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		from := cPrecisionFile(e.FromId)
		if !strings.HasSuffix(from, ".c") {
			continue
		}
		callsFromC++
		if e.Method == kgtypes.EdgeMethodDynamic {
			dynamicEdges++
			continue
		}
		if e.Method != string(parser.RuleTypedQualifier) {
			continue
		}
		boundTyped++
		member := cPrecisionMember(e.ToId)
		byShape[member]++
		// THE WRONG-TARGET RULE: a bound dispatch must land on a member whose
		// NAME the source actually dispatched somewhere in this file. A bound
		// edge to a member nobody called through is a wrong target by
		// construction, and it is the class the dynamic-rung knob and the
		// exactly-one gates exist to exclude.
		if member == "" || !dispatched[from][member] {
			wrongTargets = append(wrongTargets, e.FromId+" -> "+e.ToId)
		}
	}

	t.Logf("corpus root: %s", root)
	t.Logf("scope: %d C source files walked, %d function-pointer field nodes created", files, fieldNodes)
	t.Logf("dispatch strata: calls_from_c=%d bound_typed_qualifier=%d dynamic_edges=%d distinct_bound_members=%d",
		callsFromC, boundTyped, dynamicEdges, len(byShape))
	t.Logf("slot-bind strata: edges by shape = %v", slotBindEdge)
	// THE TARGET-KIND DISTRIBUTION IS THE RESIDUAL-CLASS MEASUREMENT. The
	// emitter deliberately asserts no third gate saying the target IS a
	// function, because declRec carries no declaration kind; this is how the
	// residue — a function-POINTER VARIABLE filling a slot rather than a
	// function — is measured rather than assumed negligible.
	kindOf := map[string]string{}
	for _, n := range res.Nodes {
		kindOf[n.Id] = n.Type
	}
	targetKinds := map[string]int{}
	for _, id := range slotTargets {
		k := kindOf[id]
		if k == "" {
			k = "<node absent>"
		}
		targetKinds[k]++
	}
	t.Logf("slot-bind strata: TARGET-KIND distribution = %v", targetKinds)
	t.Logf("wrong targets: %d %v", len(wrongTargets), cPrecisionSample(wrongTargets))

	// THE KNOB'S EFFECT, ASSERTED ON REAL SOURCE rather than on a fixture: C
	// emits no open-set group at all, which is what removes the false self-call
	// class the curl measurement found.
	assert.Zerof(t, dynamicEdges,
		"C's dynamic rung is off, so no C reference may emit an open-set group; got %d", dynamicEdges)

	// THE BAR.
	assert.Emptyf(t, wrongTargets,
		"every bound dispatch must land on a member the source dispatched in that file; %d did not", len(wrongTargets))

	// KNOWN-POSITIVE CONTROLS. Without these a corpus that produced nothing —
	// a walk that found no files, a query that stopped matching — would satisfy
	// both assertions above perfectly.
	require.Positive(t, files, "control: the walk found C source files")
	require.Positive(t, fieldNodes,
		"control: the corpus declares function-pointer struct fields, so the dispatch capture has targets to bind to")
	require.Positive(t, callsFromC, "control: C files emitted CALLS edges at all")
}

// cPrecisionCorpusFacts chunks the corpus and returns, per file, the set of
// member names the source DISPATCHED through a qualified callee, plus the count
// of function-pointer field nodes and of C files walked.
func cPrecisionCorpusFacts(t *testing.T, root string) (map[string]map[string]bool, int, int) {
	t.Helper()
	var paths []string
	//nolint:gosec // the root is a pinned local corpus the operator names to run this measurement, and the walk only reads
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".c", ".h":
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			paths = append(paths, rel)
		}
		return nil
	}))
	require.NotEmpty(t, paths, "control: the corpus walk found C source at all")

	results, _, err := parser.ChunkFiles(context.Background(), root, paths)
	require.NoError(t, err)

	dispatched := map[string]map[string]bool{}
	fieldNodes, files := 0, 0
	for _, res := range results {
		if res.Language != treesitter.LangC {
			continue
		}
		files++
		for _, ch := range res.Chunks {
			if ch.ChunkType == "field_declaration" && ch.ParentName != "" {
				fieldNodes++
			}
		}
		for i := range res.Edges {
			e := &res.Edges[i]
			if e.Type != treesitter.EdgeCalls {
				continue
			}
			// The callee text carries the qualifier; the member is what a bound
			// edge must land on.
			if member := cPrecisionMember(e.ToID); member != "" && member != e.ToID {
				if dispatched[res.FilePath] == nil {
					dispatched[res.FilePath] = map[string]bool{}
				}
				dispatched[res.FilePath][member] = true
			}
		}
	}
	return dispatched, fieldNodes, files
}

// cPrecisionFile returns the file-path half of a node ID.
func cPrecisionFile(nodeID string) string {
	if i := strings.LastIndex(nodeID, ":"); i >= 0 {
		return nodeID[:i]
	}
	return nodeID
}

// cPrecisionMember returns the final segment of a dotted or arrowed spelling —
// the member a dispatch names.
func cPrecisionMember(s string) string {
	if i := strings.LastIndex(s, "->"); i >= 0 {
		return s[i+2:]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// cPrecisionSample caps a failure sample so a large corpus reports a readable
// line rather than thousands.
func cPrecisionSample(all []string) []string {
	const cap = 10
	if len(all) <= cap {
		return all
	}
	return all[:cap]
}
