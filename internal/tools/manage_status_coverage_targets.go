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
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
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
// THE RULE EXCLUDED THE BUILT-IN LOG FAMILY, and that family is now RETIRED, so
// the union below currently excludes no builtin at all. The argument is kept
// because it is the argument the custom half still makes. A log graph is never
// summarized, never embedded and never synced, so every coverage column it could
// occupy is structurally zero for its whole life. Walking one renders a permanently
// 0%-covered row that reads identically to a knowledge or code graph whose pipeline
// has completely failed, and sends an operator to manage(rebuild_segments), which
// refuses for a family with no rebuildable segments — so the row looks actionable
// with no action behind it. A per-query log graph is also an ephemeral artifact, so
// both the row count and the one Stats RPC each row costs would grow without bound
// as a user runs log queries. A contrib collector that registers a log family
// declaring neither syncable nor embedding is filtered by the custom half on
// exactly this reasoning.
//
// EVERY REGISTERED CUSTOM FAMILY IS WALKED, appended after the builtins. It used
// to be absent from this table entirely — not filtered out, never asked about —
// so a user who registered a graph family could not see it on the one surface
// that inventories what this machine holds, which is a custom-only exception
// rather than a rule. The builtin half of the list is UNCHANGED and comes FIRST:
// the order is load-bearing (codeTypeIndex reads a position out of it for the
// overlay round), so registered families are APPENDED rather than merged or
// sorted in.
//
// THE CUSTOM HALF CARRIES NO FILTER OF ITS OWN, and the filter it used to carry
// was a defect rather than a parallel of the builtin union. See
// coverageRegisteredCustomFamilies for the argument and for the live observation
// that retired it.
//
// A CATALOG READ FAILURE IS RETURNED, NOT SWALLOWED, and the builtin half is
// returned beside it. See coverageRegisteredCustomFamilies for why the two halves
// of "no custom types" are different answers.
func coverageWalkTypes(ctx context.Context, deps ClientDeps) ([]coverageFamily, error) {
	names := kgtypes.BuiltinGraphTypeNames()
	out := make([]coverageFamily, 0, len(names))
	for _, n := range names {
		gt := kgtypes.GraphType(n)
		if !kgtypes.SyncEligible(gt) && !kgtypes.HasRebuildableSegments(gt) {
			continue
		}
		out = append(out, coverageFamily{gt: gt})
	}
	custom, err := coverageRegisteredCustomFamilies(ctx, deps)
	return append(out, custom...), err
}

// coverageFamily is one FAMILY the coverage table walks, ahead of the per-family
// instance enumeration that turns it into rows.
//
// IT EXISTS BECAUSE A REGISTERED FAMILY CAN HAVE NO INSTANCE and still owe a row,
// which a bare GraphType cannot express: the row is built from the REGISTRATION
// rather than from a graph, so the two facts the registration carries have to
// survive the walk.
type coverageFamily struct {
	gt kgtypes.GraphType
	// registered marks a family that exists because an operator REGISTERED it — a
	// config entry or a catalog record — rather than a builtin the binary ships.
	// Only a registered family gets a row with no instance behind it: a builtin
	// with no instance is a family nothing has ever used, and rendering seven such
	// rows on every status would bury the graphs that do exist.
	registered bool
	// declaresEmbedding is the REGISTRATION's own answer to whether this family's
	// nodes are embedded anywhere in the cascade — graph level or a single
	// node-type override. It is the only thing that can be known about a family
	// nothing has collected, and it is what the no-instance row's segment cell is
	// read from: kgtypes.HasRebuildableSegments answers about BUILTIN types and
	// returns true for every custom name, so asking it here would assert a pool for
	// a family that declares it embeds nothing.
	declaresEmbedding bool
}

// coverageRegisteredCustomFamilies returns every REGISTERED custom graph family
// the coverage table walks. It is the COVERAGE-WALK COUNTERPART of
// kgtypes.BuiltinGraphTypeNames(), and the sibling of syncableCustomTypes
// (sync_list.go), which does the same job for sync list under sync's own narrower
// rule.
//
// IT FILTERS NOTHING, AND THE FILTER IT USED TO CARRY WAS THE DEFECT. The rule was
// a union mirroring the builtin one — walk a family declaring SYNCABLE, or
// declaring EMBEDDING anywhere in the cascade — on the argument that a family
// declaring neither has every coverage column structurally zero for its whole
// life, the same argument the builtin half made for the retired built-in log
// family. The live confirmation of the contrib collectors showed what that costs:
// two families collected identically, both landing fifteen nodes, both rendered
// by custom_collector(list), and only the one declaring embedding appeared here.
// A REAL GRAPH HOLDING REAL NODES WAS ABSENT FROM THE INVENTORY, which is the
// same silent deletion the unmanaged row already refuses to make — "dropping it
// would silently delete the graph from the inventory manage(status) exists to
// show". The row is about COVERAGE, not about work owed: a family with nothing to
// summarize and nothing to embed still holds nodes an operator needs to see, and
// its structurally-zero columns are a true statement about it rather than a
// reason to omit it.
//
// The builtin half keeps its union, and the two are no longer parallel on purpose.
// That union decides which BUILTIN TYPES are walked at all, and a builtin type
// nothing has collected under contributes no row either way; this half decides
// whether a family AN OPERATOR REGISTERED is on the inventory, which is a
// different question with a different answer.
//
// IT READS THE UNION OF THE CONFIG FILES AND THE SERVER CATALOG, THE ENTRY
// WINNING. The config file is the registration record, so a family whose entry
// exists gets its row with NO collect ever having run — the catalog holds a
// record only once something has been collected under it. The catalog half is
// kept beside it so a family collected under an entry since removed still shows
// its rows. THAT IS A STATUS ROW, NOT A RESOLUTION: this table dispatches
// nothing, so listing a legacy family here does not make it collectable, and the
// collect path still refuses it.
//
// THE TWO WAYS TO HAVE NO CUSTOM TYPES ARE DIFFERENT ANSWERS AND ARE ANSWERED
// DIFFERENTLY, which is the whole of this function's error contract.
//
// A nil GraphTypeCRUD is CAPABILITY ABSENCE: a degraded client legitimately has
// no registration catalog wired, there is nothing to read and nothing failed, so
// it yields no catalog types and no error, exactly as it does on the sync-list
// path. The file half is unaffected by it.
//
// A CONFIG FILE THAT CANNOT BE READ IS A HARD FAILURE, not the optional-catalog
// disposition below: the file IS the record, so an unreadable one means the set
// of registered families is unknown rather than empty.
//
// A List ERROR IS A FAILURE AND IS RETURNED. The capability is there and the
// answer could not be obtained, so reporting it as "no registered families"
// would render a coverage table that LOOKS COMPLETE while silently omitting
// every one of them — the operator cannot tell a machine with no registrations
// from a machine whose catalog is unreachable. The repository's rule is that bad
// input always errors and never silently degrades, and that a default-on-error
// path fails loudly, naming the condition and what was dropped. The caller
// renders it on the status surface beside the builtin rows it did obtain, rather
// than failing the whole command: manage(status) is the operator's inventory,
// and an OPTIONAL catalog failure is no reason to deny them every builtin fact
// at the moment they are diagnosing.
//
// The BUILTIN half is unaffected on both paths and is returned by the caller
// either way.
func coverageRegisteredCustomFamilies(ctx context.Context, deps ClientDeps) ([]coverageFamily, error) {
	loader, err := collectorLoader(ctx, deps)
	if err != nil {
		return nil, err
	}
	winners, err := loader.Winners()
	if err != nil {
		return nil, err
	}
	var out []coverageFamily
	fromFile := make(map[string]struct{}, len(winners))
	for _, se := range winners {
		// An empty family name is bad input, never a family to walk: it would
		// build a selector naming no graph and spend an RPC discovering that.
		if se.Name == "" {
			continue
		}
		fromFile[se.Name] = struct{}{}
		out = append(out, newCoverageFamily(se.Name, collectorconfig.Persisted(se.Name, se.Entry)))
	}

	crud := deps.GraphTypeCRUD()
	if crud == nil {
		return out, nil
	}
	defs, err := crud.List(ctx)
	if err != nil {
		return out, fmt.Errorf("registered custom graph families are missing from this table: listing the registration catalog failed: %w", err)
	}
	for _, d := range defs {
		if d.GetName() == "" {
			continue
		}
		if _, byEntry := fromFile[d.GetName()]; byEntry {
			continue // the entry is the record; it was already counted, whole.
		}
		out = append(out, newCoverageFamily(d.GetName(), d))
	}
	return out, nil
}

// newCoverageFamily builds one registered family's walk entry from its
// registration record.
func newCoverageFamily(name string, d *knowledgev1.GraphTypeDef) coverageFamily {
	return coverageFamily{
		gt:                kgtypes.GraphType(name),
		registered:        true,
		declaresEmbedding: declaresEmbeddingAnywhere(d),
	}
}

// declaresEmbeddingAnywhere resolves the EMBEDDING half of one registration
// record's behavior cascade: the graph-level default, or a single node-type
// override turning it on.
//
// IT MIRRORS THE EFFECTIVE PER-NODE CASCADE (the server's coalesceBool resolves
// node override over graph default over false) rather than the graph-level flag
// alone, because a family embedding exactly one of its node types really does
// carry a segment pool — the same way web and pdf are admitted by the builtin
// segment half while never syncing.
//
// IT IS NO LONGER A FILTER. It used to decide whether the family was walked at
// all; now it decides only what a NO-INSTANCE row's segment cell says, which is
// the one question a registration can answer about a family nothing has
// collected.
func declaresEmbeddingAnywhere(d *knowledgev1.GraphTypeDef) bool {
	if d.GetBehavior().GetEmbeddable() {
		return true
	}
	for _, override := range d.GetNodeTypes() {
		if override.GetEmbeddable() {
			return true
		}
	}
	return false
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
// IT RETURNS THE ROWS IT DID OBTAIN ALONGSIDE ANY ERROR, rather than one or the
// other. The only error it can produce is a registration-catalog read failure,
// which costs the CUSTOM half of the walk and nothing else, so returning no rows
// would throw away every builtin row over an optional capability. The caller
// renders both.
func coverageTargets(ctx context.Context, deps ClientDeps) ([]coverageTarget, error) {
	families, walkErr := coverageWalkTypes(ctx, deps)
	perType := make([][]catalogEntry, len(families))
	var wg sync.WaitGroup
	for i, fam := range families {
		if fam.gt == kgtypes.GraphKnowledge {
			// Emitted explicitly below via the empty-name selector; enumerating
			// it again would skip the empty-name default and/or double-count.
			continue
		}
		wg.Go(func() {
			entries, err := listCatalogOfType(ctx, deps, string(fam.gt))
			if err != nil {
				return
			}
			perType[i] = entries
		})
	}
	wg.Wait()

	var codeBases []string
	if ci := codeTypeIndex(families); ci >= 0 {
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
	for i, fam := range families {
		for _, e := range perType[i] {
			t := newCoverageTarget(fam.gt, e.name, false)
			t.managed = inWorkingSetFor(deps, fam.gt, e.name)
			t.imageBytes = e.imageBytes
			targets = append(targets, t)
		}
		// A REGISTERED FAMILY WITH NO INSTANCE STILL GETS ITS ROW. The enumeration
		// above is per GRAPH, and a family nothing has collected under has no graph
		// — which is exactly the state a family spends between `collector add` and
		// its first collect, and the state the live confirmation found eight
		// families in while every other surface rendered them. The row names the
		// family with its instance half empty and is built from the registration,
		// because the registration is the only record there is.
		if fam.registered && len(perType[i]) == 0 {
			targets = append(targets, newRegisteredFamilyTarget(fam))
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
	return targets, walkErr
}

// newCoverageTarget builds one row's target from its type and instance name. It is
// the SINGLE producer of the row label, so a base row and a branch row cannot drift
// into two spellings of the same identity.
func newCoverageTarget(gt kgtypes.GraphType, name string, overlay bool) coverageTarget {
	return coverageTarget{
		label: fmt.Sprintf("%s/%s", gt, name),
		gt:    gt,
		name:  name,
		// statusGraphTarget rather than a bare derivation: this table reports a row
		// PER NAMED GRAPH, and practice is a singleton whose derived selector
		// carries no instance field — every legacy practice row would ask about the
		// combined graph and print the same numbers under a different name.
		target:  statusGraphTarget(gt, name),
		overlay: overlay,
	}
}

// newRegisteredFamilyTarget builds the row target for a REGISTERED family with no
// collected instance.
//
// THE LABEL IS THE BARE FAMILY NAME, with no "/instance" half, and that is the
// row's whole claim: this machine knows the family and holds no graph under it.
// Composing "jira/" or inventing an instance name would put an identity on the
// inventory that nothing can be addressed by.
//
// IT CARRIES NO Stats SELECTOR because there is no graph to ask about; the
// assembly walk skips both the Stats RPC and the segment probe for it (see
// collectCoverageRows). managed stays false for the honest reason rather than by
// omission: nothing has been collected, so no direct interaction has admitted it
// into this client's working set.
func newRegisteredFamilyTarget(fam coverageFamily) coverageTarget {
	return coverageTarget{
		label:            string(fam.gt),
		gt:               fam.gt,
		noInstance:       true,
		declaredSegments: fam.declaresEmbedding,
	}
}

// codeTypeIndex reports where the code family sits in the walked family order,
// so the overlay round reads the base list the first round filled for it. Returns
// -1 when code is not eligible, which yields no overlay round at all.
func codeTypeIndex(families []coverageFamily) int {
	for i, fam := range families {
		if fam.gt == kgtypes.GraphCode {
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
