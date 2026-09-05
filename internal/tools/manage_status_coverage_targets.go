// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_targets.go — the coverage table's ENUMERATION half: which
// graph types are walked, which instances of them the table reports on, and the
// second round that discovers each code base graph's branch overlays.
//
// SPLIT FROM manage_status_coverage_collect.go FOR THE 500-LINE CAP, along the seam
// that file already had: its sibling keeps the seam READERS (the optional
// stall/working-set capabilities and the backstop lookup) and the bounded Stats
// fan-out that reads counts, while WHICH GRAPHS EXIST AT ALL now lives here. The
// enumeration grew when it stopped walking the sync-eligible subset, which is what
// pushed the file over. Same package, no signature changed.
package tools

import (
	"context"
	"fmt"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// coverageWalkTypes is the type set the coverage table walks. THE RULE IS A UNION:
// a builtin is walked when it is SYNC-ELIGIBLE **OR** SEGMENT-BEARING. Those two
// predicates are what the table's two column families report on, so a builtin in
// neither has nothing any column could say about it.
//
// IT USED TO WALK SyncEligibleGraphTypes(), AND THAT WAS A SUBSET BUG THE MOMENT
// RAW GRAPHS GAINED SEGMENTS. While HasRebuildableSegments was derived from
// SyncEligible the two sets nested, so walking the sync-eligible types and letting
// the segment cell decide covered everything that could have a segment. Web and pdf
// are now segment-bearing and are NOT sync-eligible — they never sync, deliberately
// — so a sync-eligible outer loop skips them entirely and no coverage row is ever
// built for a collected document, on any backend. That is the ticket's own
// verification line ("manage status shows binary vectors > 0 and segment coverage
// for the pdf graph") failing at the enumeration rather than at the probe.
//
// BOTH HALVES OF THE UNION ARE LOAD-BEARING, each in a direction the other cannot
// reach, which is why the rule is a union and not either predicate alone:
//   - SyncEligible alone drops web and pdf — the subset bug above.
//   - HasRebuildableSegments alone drops LINKAGE and TRANSFORMERS, which carry no
//     rebuildable segments but DO have durable LLM-coverage counts and render a row
//     today, so it would repair the raw graphs by breaking those two.
//
// The segment cell decides its own content per row (segCoveredFor consults
// HasRebuildableSegments), so a walked non-segment graph keeps its counts and
// renders a dashed segment cell exactly as before.
//
// LOGS IS THE TYPE THE RULE EXCLUDES, and the exclusion is the point rather than an
// oversight — it is the only builtin failing both halves. A logs graph is never
// summarized, never embedded and never synced, so every coverage column it could
// occupy is structurally zero for its whole life. Walking it renders a permanently
// 0%-covered row that reads identically to a knowledge or code graph whose pipeline
// has completely failed, and sends an operator to manage(rebuild_segments
// graph:"logs"), which refuses — rebuildableBuiltinNames() excludes logs, so the row
// looks actionable with no action behind it. Log graphs are also ephemeral per-query
// artifacts enumerated from disk, so both the row count and the one Stats RPC each
// row costs would grow without bound as a user runs log queries.
func coverageWalkTypes() []kgtypes.GraphType {
	names := kgtypes.BuiltinGraphTypeNames()
	out := make([]kgtypes.GraphType, 0, len(names))
	for _, n := range names {
		gt := kgtypes.GraphType(n)
		if !kgtypes.SyncEligible(gt) && !kgtypes.HasRebuildableSegments(gt) {
			continue
		}
		out = append(out, gt)
	}
	return out
}

// coverageTargets enumerates every graph instance the coverage table covers, in
// the table's deterministic order: the default knowledge graph first (explicit
// empty-name selector — its empty instance name is dropped by
// listGraphNamesOfType), then every other BUILTIN graph type in order, each
// instance in enumeration order, and finally every code BRANCH GRAPH in base order
// then enumeration order. The per-type name enumerations are independent RPCs, so
// they run concurrently; a failed enumeration drops that type's rows, same as the
// historical sequential walk.
//
// THE BRANCH GRAPHS ARE A SECOND ROUND because their enumeration depends on the
// base list the first round produces: each base's overlays are listed by asking the
// SAME RETURN_MODE_GRAPH_NAMES seam with overlay_of set. Without it the enumeration
// returns base instances only and a first-class branch graph appears on no
// inventory surface at all.
func coverageTargets(ctx context.Context, deps ClientDeps) []coverageTarget {
	types := coverageWalkTypes()
	perType := make([][]catalogEntry, len(types))
	var wg sync.WaitGroup
	for i, gt := range types {
		if gt == kgtypes.GraphKnowledge {
			// Emitted explicitly below via the empty-name selector; enumerating
			// it again would skip the empty-name default and/or double-count.
			continue
		}
		wg.Go(func() {
			entries, err := listCatalogOfType(ctx, deps, string(gt))
			if err != nil {
				return
			}
			perType[i] = entries
		})
	}
	wg.Wait()

	var codeBases []string
	if ci := codeTypeIndex(types); ci >= 0 {
		codeBases = catalogNames(perType[ci])
	}
	overlayKeys := coverageOverlayKeys(ctx, deps, codeBases)

	targets := []coverageTarget{{
		label: "knowledge",
		gt:    kgtypes.GraphKnowledge,
		// The Stats SELECTOR uses the empty instance name (that is the stats wire
		// contract for the default graph), but the segment probe is a different key
		// space: the default knowledge graph's segments live under "default", which
		// the segment reconcile seeds explicitly for this exact reason — the default
		// instance enumerates an empty name that ListGraphNamesOfType drops. Leaving
		// this empty probes a key nothing writes, reporting the primary corpus as
		// uncovered however well covered it is, and makes the reader lazily
		// construct a manager for an instance that does not exist.
		name:   "default",
		target: &knowledgev1.GraphSelector{Graph: ""},
		// Membership is asked about "default" — the same name the segment probe uses
		// — because the working set normalizes knowledge's "" and "default" to one
		// Ref, so the two spellings cannot become two different answers.
		managed: inWorkingSetFor(deps, kgtypes.GraphKnowledge, "default"),
	}}
	for i, gt := range types {
		for _, e := range perType[i] {
			t := newCoverageTarget(gt, e.name, false)
			t.managed = inWorkingSetFor(deps, gt, e.name)
			t.imageBytes = e.imageBytes
			targets = append(targets, t)
		}
	}
	for i, base := range codeBases {
		for _, key := range overlayKeys[i] {
			bare := bareOverlayName(base, key)
			if bare == "" {
				continue
			}
			// A key STILL carrying an "@" after normalization did not belong to
			// this base — the enumeration is base-scoped, so this is defensive.
			// Recomposing one would fabricate a graph identity in an inventory row.
			if left, _, ok := atSplit(bare); ok && left != base {
				continue
			}
			bt := newCoverageTarget(kgtypes.GraphCode, base+"@"+bare, true)
			// A branch row's ADMISSION follows its base's — the working set cuts a name
			// at the first "@", so this asks about the base, which is the graph a
			// collect would have admitted when it produced the branch.
			bt.managed = inWorkingSetFor(deps, kgtypes.GraphCode, base)
			targets = append(targets, bt)
		}
	}
	return targets
}

// newCoverageTarget builds one row's target from its type and instance name. It is
// the SINGLE producer of the row label, so a base row and a branch row cannot drift
// into two spellings of the same identity.
func newCoverageTarget(gt kgtypes.GraphType, name string, overlay bool) coverageTarget {
	return coverageTarget{
		label:   fmt.Sprintf("%s/%s", gt, name),
		gt:      gt,
		name:    name,
		target:  graphsel.GraphSelectorFor(gt, name, false),
		overlay: overlay,
	}
}

// codeTypeIndex reports where the code type sits in the walked type order,
// so the overlay round reads the base list the first round filled for it. Returns
// -1 when code is not eligible, which yields no overlay round at all.
func codeTypeIndex(types []kgtypes.GraphType) int {
	for i, gt := range types {
		if gt == kgtypes.GraphCode {
			return i
		}
	}
	return -1
}

// coverageOverlayKeys enumerates each code base graph's overlay keys, one bounded
// goroutine per base, and returns them BY BASE INDEX so the row order stays
// deterministic however the enumerations interleave.
//
// THE BOUND IS OWED HERE IN A WAY IT IS NOT OWED BY THE PER-TYPE ROUND. That round's
// width is bounded by the builtin type count, a compile-time constant; this one's
// width is the number of code base graphs, which is user data and unbounded — an
// install with fifty repos would otherwise open fifty concurrent enumerations
// from a single status call. It reuses the Stats fan-out's own semaphore idiom and
// its coverageStatsConcurrency bound rather than introducing a second number.
//
// THE ENUMERATION IS CODE-ONLY, and not merely by preference. Overlays of the other
// families are knowledge session overlays — ephemeral working state rather than
// inventory — and the server's selector validation rejects a knowledge selector
// whose name is not a root alias, so such a target would error and drop its own row
// after doing the work.
//
// A failed enumeration leaves that base's slice nil and drops only that base's branch
// rows, matching the failure semantics of the per-type enumeration above.
func coverageOverlayKeys(ctx context.Context, deps ClientDeps, codeBases []string) [][]string {
	keys := make([][]string, len(codeBases))
	var wg sync.WaitGroup
	sem := make(chan struct{}, coverageStatsConcurrency)
	for i, base := range codeBases {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			found, err := listOverlayKeysOfBase(ctx, deps, string(kgtypes.GraphCode), base)
			if err != nil {
				return
			}
			keys[i] = found
		})
	}
	wg.Wait()
	return keys
}
