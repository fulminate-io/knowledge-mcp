// SPDX-License-Identifier: Apache-2.0

// lane_resolution.go — turning the NAME a subagent lane was spawned under into the id the
// cache keys that lane by.
//
// The two spellings are different strings and neither derives from the other except through
// the shared name: an operator knows the lane as "planner", while the cache stores its rows
// under agent id "aplanner-<16 hex>". Selecting a lane by the name therefore needs a lookup
// against the cache, and the lookup reads ROWS rather than cache file names: a lane's parent
// SESSION — which scopes the lookup and which an ambiguity refusal must name — lives in the
// rows and is absent from the file name.
package transcriptanalytics

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// laneIDHexLen is the width of the hex suffix the CLI appends to a spawn name to build a
// lane's agent id. It is what makes the forward construction (name → id) unambiguous even
// though the reverse parse is not: a name may itself contain hyphens, so only the fixed-width
// trailing field identifies the boundary.
const laneIDHexLen = 16

// LaneCandidate is one cached lane a spawn name resolved to: the cache's own agent id for
// it, and every parent session its rows carry. The sessions are what let a caller tell two
// same-named lanes apart, so they are listed rather than reduced to one.
type LaneCandidate struct {
	AgentID    string
	SessionIDs []string
}

// LaneNameResolution is the outcome of resolving a spawn name against the cache.
//
// LaneCount is the number of cached lane files, taken from the glob BEFORE any matching, so
// it distinguishes an empty cache from a name that matched nothing on a populated one.
// Those are different answers to the caller — one says "populate the cache", the other says
// "that selector is wrong" — and a resolver that reported only its candidates could not tell
// them apart.
type LaneNameResolution struct {
	LaneCount  int64
	Candidates []LaneCandidate
}

// IsLaneID reports whether an agent selector is the cache's own lane id spelling
// (a<name>-<16 lowercase hex>) rather than a spawn name.
//
// It is the fork the whole selector rule turns on: an id is used as given, a name is
// resolved. A predicate loose enough to read a name as an id would leave that lane
// unresolvable while it sits in the cache; one loose enough to read an id as a name would
// send a unique selector through a lookup that cannot improve it. The name may contain
// hyphens, so the boundary is the fixed-width trailing hex field, not the first hyphen.
func IsLaneID(agent string) bool {
	if !strings.HasPrefix(agent, "a") {
		return false
	}
	cut := strings.LastIndex(agent, "-")
	if cut < 2 { // "a" plus at least one character of name, then the separator
		return false
	}
	return isLowerHex(agent[cut+1:], laneIDHexLen)
}

// isLowerHex reports whether s is exactly width lowercase hex digits. Uppercase is rejected
// deliberately: the CLI writes lowercase, so an uppercase suffix is some other string that
// happens to look similar, and accepting it would resolve a selector the cache cannot hold.
func isLowerHex(s string, width int) bool {
	if len(s) != width {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// laneIDForName reports whether agentID is the cache id of a lane spawned as the name whose
// prefix is given ("a" + name + "-"). The prefix is built once by the caller because this
// runs per row over the whole cache.
func laneIDForName(agentID, prefix string) bool {
	rest, ok := strings.CutPrefix(agentID, prefix)
	return ok && isLowerHex(rest, laneIDHexLen)
}

// ResolveLaneName resolves the NAME a lane was spawned under to the cache lane ids carrying
// it, scoped to sessionID when one is given and to the whole cache otherwise.
//
// It returns EVERY candidate rather than choosing one. A spawn name is reused across
// sessions while the cache id is unique, so picking a winner would silently analyze a
// different lane than the caller named — a report that is entirely plausible and about the
// wrong subject, which is the failure this package exists to refuse.
//
// COST: one decode of the cache, paid only by a by-name call; a by-id call never reaches
// here. The lane count comes from the glob alone, so an empty cache costs one directory read
// and no decode at all.
func (s *Service) ResolveLaneName(ctx context.Context, name, sessionID string) (LaneNameResolution, error) {
	if name == "" {
		return LaneNameResolution{}, fmt.Errorf("transcriptanalytics: resolving a lane requires a non-empty name")
	}
	paths, err := s.cachePaths()
	if err != nil {
		return LaneNameResolution{}, err
	}
	res := LaneNameResolution{LaneCount: int64(len(paths))}
	if len(paths) == 0 {
		return res, nil
	}

	prefix := "a" + name + "-"
	partials := make([]map[string]map[string]bool, len(paths))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i, p := range paths {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			rows, err := transcripts.ReadSessionParquet(p)
			if err != nil {
				// Attributed to THIS package, in key with every other error it returns: a
				// resolution that failed to read a cache file must not reach the caller as an
				// empty candidate list, which reads as "no lane by that name".
				return fmt.Errorf("transcriptanalytics: read cache file %q: %w", p, err)
			}
			found := make(map[string]map[string]bool)
			for j := range rows {
				id := rows[j].AgentID
				if !laneIDForName(id, prefix) {
					continue
				}
				if found[id] == nil {
					found[id] = make(map[string]bool)
				}
				found[id][rows[j].SessionID] = true
			}
			partials[i] = found
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return LaneNameResolution{}, err
	}

	res.Candidates = mergeLaneCandidates(partials, sessionID)
	return res, nil
}

// mergeLaneCandidates folds the per-file matches into one sorted candidate list, keeping only
// the lanes present in sessionID when one was given.
//
// A lane is keyed by its AGENT ID, never by an (id, session) pair: one lane whose rows name
// two parent sessions is still one lane, and splitting it would report a resolvable name as
// ambiguous. The session filter drops a candidate whole; the sessions it reports are all of
// them, so a refusal names what the lane actually carries rather than the one that matched.
func mergeLaneCandidates(partials []map[string]map[string]bool, sessionID string) []LaneCandidate {
	merged := make(map[string]map[string]bool)
	for _, part := range partials {
		for id, sessions := range part {
			if merged[id] == nil {
				merged[id] = make(map[string]bool)
			}
			for sess := range sessions {
				merged[id][sess] = true
			}
		}
	}
	out := make([]LaneCandidate, 0, len(merged))
	for id, sessions := range merged {
		if sessionID != "" && !sessions[sessionID] {
			continue
		}
		names := make([]string, 0, len(sessions))
		for sess := range sessions {
			names = append(names, sess)
		}
		sort.Strings(names)
		out = append(out, LaneCandidate{AgentID: id, SessionIDs: names})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}
