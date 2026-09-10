// SPDX-License-Identifier: Apache-2.0

// collect_recipe_land_version.go — how a landing finds the LATEST version of each
// emitted id, and why finding it takes more than one read.
//
// THE DEFECT THIS FILE EXISTS TO CLOSE. A twin is stored under a DERIVED id, not
// under the emitted (base) id, so a collision read built from the emitted ids
// alone only ever sees version 1. A second landing then finds the v1 resident and
// mints v2; a THIRD landing finds the same v1 resident and mints the SAME v2 id
// again — an add-not-upsert write over the twin the second run created. The
// counter pinned at 2 and "both are retained" stopped holding at the third run.
//
// THE CHOICE, and it is a choice: the chain is walked by PROBING DERIVED IDS,
// level by level, rather than by traversing the next-version edges. Both are in
// the file's own words in landingVersionChains.

package tools

import (
	"context"
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// landingResidency is the LATEST version of one emitted base id already stored in
// the combined graph: the id to hang the next next-version edge off, and the
// integer the next twin increments.
//
// THE VERSION IS STRUCTURAL, read off the chain's DEPTH rather than off the
// stored `version` metadata. The twin's id is derived WITH its version inside it,
// so depth and id agree by construction; trusting a stored integer instead would
// let one hand-edited string mint an id that collides with a chain member that
// already exists, which is the overwrite this whole design refuses. The metadata
// key stays written, because a reader of one node needs its version without
// walking anything — it is a disclosure of the chain position, never its source.
type landingResidency struct {
	latestID      string
	latestVersion int
}

// landingVersionChains resolves, for every emitted id, whether the combined graph
// already holds it and if so which version is the LATEST.
//
// WHY PROBING DERIVED IDS RATHER THAN TRAVERSING next-version EDGES. Both reach
// the same answer and the difference is read cost. A twin id is a pure function
// of (target key, slug, kind, base id, version), so level v+1's candidate id is
// computable for every chain still alive at level v — which makes each level ONE
// bulk by-ids read for the whole population, and the whole walk 1 + (L-1) bulk
// reads where L is the deepest version any emitted id has reached. An edge
// traversal has no such batching: the client's foundation seam walks edges from
// ONE start node, so a document whose rows all collide would pay one round trip
// per row per level — 467 reads for the tester's measured document where this
// pays two. The edge is still written, because requirement 4 names it and a
// reader follows it; it is simply not what the landing itself reads.
//
// TERMINATION is structural rather than capped: level v+1 is probed only for
// chains that were FOUND at level v, every level's ids are distinct (the version
// is inside the hash), and a level that finds nothing ends the walk. So the loop
// is bounded by the deepest chain actually stored.
func landingVersionChains(
	ctx context.Context, deps ClientDeps, a collectArgs, pre *landingPreflight, nodes []*knowledgev1.Node,
) (map[string]landingResidency, error) {
	kinds := make(map[string]string, len(nodes))
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if _, seen := kinds[n.GetId()]; seen {
			continue
		}
		kinds[n.GetId()] = n.GetType()
		ids = append(ids, n.GetId())
	}
	sort.Strings(ids) // deterministic page boundaries across runs

	base, err := landingResidentRead(ctx, deps, a, ids)
	if err != nil {
		return nil, err
	}
	chains := make(map[string]landingResidency, len(base))
	for id := range base {
		chains[id] = landingResidency{latestID: id, latestVersion: 1}
	}

	for version := 2; ; version++ {
		probe := make([]string, 0, len(chains))
		origin := make(map[string]string, len(chains))
		for baseID, r := range chains {
			if r.latestVersion != version-1 {
				continue
			}
			cand := recipe.VersionedTwinID(landingTargetKey(), pre.slug, kinds[baseID], baseID, version)
			probe = append(probe, cand)
			origin[cand] = baseID
		}
		if len(probe) == 0 {
			return chains, nil
		}
		sort.Strings(probe)
		found, ferr := landingResidentRead(ctx, deps, a, probe)
		if ferr != nil {
			return nil, ferr
		}
		if len(found) == 0 {
			return chains, nil
		}
		for cand := range found {
			chains[origin[cand]] = landingResidency{latestID: cand, latestVersion: version}
		}
	}
}

// landingResidentRead is ONE bulk by-ids hydrate against the target graph, with
// the two policies this caller owns stated here rather than inherited.
//
// TOMBSTONES ARE INCLUDED, DELIBERATELY. A soft-deleted resident still HOLDS its
// id, so reading it as absent would land a node under an id a tombstoned shadow
// occupies. Requirement 6's soft-by-default by-hub delete makes that state
// reachable in ordinary use.
//
// A TRUNCATION VERDICT IS FATAL. FetchNodesByIDs reports truncation rather than
// acting on it, because a renderer legitimately shows a short list as short. A
// landing is the other kind of caller: a partial map means residents it did not
// see, and the add-not-upsert write would then overwrite exactly those.
func landingResidentRead(
	ctx context.Context, deps ClientDeps, a collectArgs, ids []string,
) (map[string]*knowledgev1.Node, error) {
	residents, truncated, err := foundation.FetchNodesByIDs(ctx, deps.GraphCaller(),
		landingTarget().GraphType, landingWireName, ids, foundation.IncludeTombstones)
	if err != nil {
		return nil, fmt.Errorf(
			"collect %s transformer=recipe land: the combined practice graph could not be read for resident nodes, "+
				"so a landing cannot tell a collision from a first write: %w", a.Type, err)
	}
	if truncated {
		return nil, fmt.Errorf(
			"collect %s transformer=recipe land: the resident read came back TRUNCATED over %d id(s), so this "+
				"landing did not see every resident node and would overwrite the ones it missed. Nothing was written. "+
				"Narrow the recipe body so it emits fewer rows, or land the document in parts",
			a.Type, len(ids))
	}
	return residents, nil
}
