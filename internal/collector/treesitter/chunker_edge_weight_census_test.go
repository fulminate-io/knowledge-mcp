// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An IMPLEMENTS edge carries its interface's method-set cardinality on Method
// and leaves Weight at ZERO, and this census is what keeps that decision
// honest by naming every file that reads an edge's Weight.
//
// WHY THE CARRIER IS NOT Weight. The weighted topology analyzers normalize a
// zero weight to the 1.0 baseline before feeding gonum, so a cardinality on
// Weight would INVERT the intent: the low-information single-method edges would
// enter weighted centrality at exactly an ordinary edge's strength, while a
// large interface's edge took many times an ordinary edge's random-walker mass.
// The claim that no WEIGHTED ANALYZER reads Method is scoped, not a complete
// enumeration of Method's readers — and this census is the standing check that
// the Weight side of that argument stays true as files move.
//
// THE PROPERTY IS "THIS FILE READS AN EDGE'S WEIGHT", and the detector is
// probed in BOTH directions per the census-detector rule. THREE classes have to
// be accounted for — two of over-match and one of under-match:
//
//	OVER-MATCH (1), THOUGHT-CHARGE WEIGHT: an unrelated quantity that happens to
//	share the field name. Two files, carried as explicit EXCLUSION rows with that
//	reason rather than silently omitted.
//	OVER-MATCH (2), THE GONUM SURFACE: `\.Weight` also matches the TYPE names
//	`.WeightedDirectedGraph` and `graph.WeightedDirected`, and gonum's own
//	`g.Weight(u, v)` METHOD. Six files match ONLY that way and none of them reads
//	an edge field.
//	UNDER-MATCH (3), THE GENERATED PROTO ACCESSOR: an edge read through
//	`e.GetWeight()` HAS the property and matches `\.Weight` not at all. SIX
//	non-test files read Weight this way, one of them a state-digest consumer
//	rather than plumbing.
//
// THE DETECTOR IS THEREFORE THE WIDENED FORM, `\.Weight|GetWeight\(`, and the
// widening is what this census exists to hold. Re-derived against the tree: the
// narrow form returns 49 files and the widened one 55, a STRICT SUPERSET adding
// exactly the six accessor readers. PROBING ONLY OVER-MATCH IS HOW A CENSUS
// STAYS GREEN WHILE READERS SLIP THROUGH IT — a regression of the detector back
// to `\.Weight` would silently drop all six with every other subtest still
// green, which is why accessor_readers_present asserts them BY NAME.
//
// SCOPE: THE OSS-SHIPPED SURFACE. Files compiled only behind the private build
// constraint are OUT of this census, and the boundary is drawn by that
// CONSTRAINT rather than by a path, so this file names no private structure.
// The reason is a standing repository rule, not a preference of this census: the
// OSS leak gate forbids a shipped source file from referencing private
// serving-architecture internals, and a disposition table enumerating those
// paths is exactly such a reference. Seven Weight readers sit behind the
// constraint; they are governed by the private tree's own review, and the walk
// skips them by reading their build line. Measured: the constraint separates
// exactly those seven from the other forty-eight, which is the row count of the
// disposition table in chunker_edge_weight_census_data_test.go — the table is no
// longer BELOW, it is next door.
//
// EVERY REASON BELOW WAS READ IN CURRENT SOURCE.
type weightReaderRow struct {
	Path        string
	Disposition testCallsDisposition
	Reason      string
}

// weightReaderDetector is the widened property probe. It is declared here, as
// the ONE definition the walk uses, so the prose above and the executed check
// cannot drift apart.
var weightReaderDetector = regexp.MustCompile(`\.Weight|GetWeight\(`)

// privateBuildConstraint matches the build line that marks a file as belonging
// to the non-shipped tree. Matching on the CONSTRAINT is what lets this census
// draw its scope boundary without naming a private path.
var privateBuildConstraint = regexp.MustCompile(`(?m)^//go:build internal$`)

// accessorOnlyWeightReaders are the files that read an edge's Weight through the
// GENERATED ACCESSOR and match a bare `\.Weight` not at all.
//
// THEY ARE NAMED, NOT COUNTED. A count leg is satisfied by any six files, so a
// detector that regressed to the narrow pattern while some unrelated six files
// drifted in would still pass. The state digest is the load-bearing member —
// the only one of the six that is not plumbing.
var accessorOnlyWeightReaders = []string{
	"cmd/knowledge-server/internal/bootstrap/engine_carrier_convert.go",
	"cmd/knowledge-server/internal/bootstrap/engine_mutate_decode.go",
	"cmd/knowledge-server/internal/bootstrap/engine_mutate_decode_predicate.go",
	"cmd/knowledge-server/internal/store/graph_state_digest.go",
	"cmd/knowledge/internal/collector/remote/sink_metrics.go",
	"cmd/knowledge/internal/engine/engine_decode.go",
}

// TestEdgeWeightConsumerCensus walks both internal trees for files reading an
// edge's Weight and requires each to carry a stated disposition.
func TestEdgeWeightConsumerCensus(t *testing.T) {
	root := repoRootForCensus(t)

	found := map[string]bool{}
	for _, tree := range []string{
		filepath.Join("cmd", "knowledge", "internal"),
		filepath.Join("cmd", "knowledge-server", "internal"),
	} {
		walkRoot := filepath.Join(root, tree)
		require.DirExists(t, walkRoot, "census control: the consumer tree exists")
		err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, readErr := os.ReadFile(path) //nolint:gosec // walks this repo's own source tree
			if readErr != nil {
				return readErr
			}
			if !weightReaderDetector.Match(body) {
				return nil
			}
			if privateBuildConstraint.Match(body) {
				// Out of scope by the repository's own OSS boundary — see the
				// SCOPE note on this census. Skipped by the CONSTRAINT so this
				// file names no private path.
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			found[filepath.ToSlash(rel)] = true
			return nil
		})
		require.NoError(t, err)
	}

	byPath := map[string]weightReaderRow{}
	for _, row := range edgeWeightConsumerCensus {
		require.NotContains(t, byPath, row.Path, "duplicate census row for %s", row.Path)
		byPath[row.Path] = row
	}

	t.Run("walk_control_fires", func(t *testing.T) {
		// Runs FIRST. Every assertion below is set agreement, and two empty sets
		// agree perfectly.
		require.NotEmpty(t, found, "census control: the walk found at least one Weight reader")
		require.NotEmpty(t, edgeWeightConsumerCensus, "census control: the disposition table is not empty")
	})

	t.Run("every_reader_has_a_row", func(t *testing.T) {
		for path := range found {
			row, ok := byPath[path]
			if !assert.True(t, ok,
				"%s reads an edge's Weight and carries NO disposition. Add a row stating opts_in, "+
					"excluded_by_decision, follow_up or producer, with the reason. A silent omission "+
					"is indistinguishable from an oversight.", path) {
				continue
			}
			assert.NotEmpty(t, strings.TrimSpace(row.Reason),
				"%s carries a disposition with no reason", path)
		}
		for path := range byPath {
			assert.True(t, found[path],
				"the census carries a row for %s, which no longer reads an edge's Weight. "+
					"Remove the row, or restore the reader.", path)
		}
	})

	t.Run("charge_weight_excluded", func(t *testing.T) {
		// THE OVER-MATCH CLASS'S NAMED-FILE CONTROL. The two thought-charge files
		// must be PRESENT as exclusion rows, not absent — an omission would leave a
		// reader of the table unable to tell "considered and excluded" from
		// "never noticed".
		for _, p := range []string{
			"cmd/knowledge/internal/tools/intercept_thoughts_charge.go",
			"cmd/knowledge/internal/tools/intercept_thoughts_simulate.go",
		} {
			row, ok := byPath[p]
			require.True(t, ok, "%s must carry an explicit exclusion row", p)
			assert.Equal(t, dispositionExcluded, row.Disposition,
				"%s reads a CHARGE's weight, not an edge's", p)
			assert.Contains(t, strings.ToLower(row.Reason), "charge",
				"%s's row must say WHY it is excluded", p)
		}
	})

	t.Run("accessor_readers_present", func(t *testing.T) {
		// THE UNDER-MATCH CLASS'S NAMED-FILE CONTROL, and the reason this census
		// uses the widened detector at all. ASSERTED BY NAME, NEVER BY COUNT: a
		// count leg is satisfied by any six files, so a detector that regressed to
		// the narrow `\.Weight` pattern could drop all six while every other
		// subtest stayed green.
		for _, p := range accessorOnlyWeightReaders {
			assert.True(t, found[p],
				"%s reads an edge's Weight through the generated accessor and matches a bare "+
					"`.Weight` not at all. The walk must use the WIDENED detector, or this reader "+
					"disappears from the census silently.", p)
		}
		// The load-bearing member, named on its own: the only one of the six that
		// is not plumbing.
		require.True(t, found["cmd/knowledge-server/internal/store/graph_state_digest.go"],
			"the state digest folds an edge's Weight into the divergence hash and MUST be censused")
		row, ok := byPath["cmd/knowledge-server/internal/store/graph_state_digest.go"]
		require.True(t, ok)
		assert.Equal(t, dispositionOptsIn, row.Disposition,
			"the state digest genuinely reads an edge's Weight; it is not an over-match")
	})
}
