// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_think_session.go — the thoughts(think) SESSION seam:
// resolve-or-create the NodeThoughtSession a thought is filed under, mint the
// composer summary a new session node needs, and read the session's current last
// thought (the EdgeNext lineage source). Split out of
// intercept_thoughts_think.go, which owns the compose/persist path itself;
// buildContextLinks (write_context_links.go) reuses the get-or-create here so the
// finding/research/rule/decision creates file under sessions identically.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// getOrCreateThoughtSessionClient resolves a session by name, store-portably:
// it issues a bounded symbol_name-EQ field-predicate browse over
// NodeThoughtSession (Selection.field_predicates, the server applies the WHERE
// where supported), then ALWAYS filters the returned rows to SymbolName==name
// client-side — defense-in-depth that stays correct even against an old or
// predicate-blind server that ignores field_predicates, so the resolver never
// attaches to a wrong-named session. On a same-name collision it returns the
// lowest id (sort.Strings, ids[0]) over the filtered set — the lowest-id tie-break
// idiom reproducing the deleted resolveSessionsByName backfill — else it creates a
// new session node and returns its id. THE DRAIN is what makes that tie-break
// deterministic: it reads bounded id-keyset pages until the corpus is exhausted,
// so every same-name session is in the filtered set before the sort runs, whatever
// order the store returns them in. The browse rides the Execute carrier
// (engine.DecodeNodes). The prior limit:0 browse — capped to browseDefaultLimit=10,
// so the existing session fell off the page once the corpus exceeded 10 sessions —
// was the duplicate-spawning defect this resolve replaces.
func getOrCreateThoughtSessionClient(ctx context.Context, gc GraphCaller, name string) (string, error) {
	sessions, derr := paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		args, merr := json.Marshal(map[string]any{
			"type": string(kgtypes.NodeThoughtSession),
			"field_predicates": []map[string]string{
				{"field": "symbol_name", "op": "eq", "value": name},
			},
			"limit": paging.BrowsePageSize,
			// SET on every page including the first, where the value is empty:
			// presence, not emptiness, is what selects the keyset browse.
			"after_id":   afterID,
			"skip_total": true,
		})
		if merr != nil {
			return nil, fmt.Errorf("marshal session browse: %w", merr)
		}
		resp, rerr := executeQuery(ctx, gc, args)
		if rerr != nil {
			return nil, fmt.Errorf("session browse: %w", rerr)
		}
		return engine.DecodeNodes(resp)
	}, paging.BrowsePageSize)
	if derr != nil {
		return "", fmt.Errorf("session decode: %w", derr)
	}
	// Client-side SymbolName guard (DEFENSE-IN-DEPTH, store-portable — do NOT
	// delete): against a server that HONORS field_predicates (the OSS embedded
	// executor via nodeMatchesField, or the cloud executor's fieldPredicateClauses)
	// the browse already returns only exact-name matches and this filter is a cheap
	// no-op over 1-2 rows; against a predicate-BLIND server that ignored
	// field_predicates and returned an arbitrary capped page, it is the load-bearing
	// guard that prevents attaching to a session of the WRONG name. Collect every
	// match id, sort, and return the lowest (the established lowest-id tie-break) so
	// duplicate same-name sessions converge deterministically.
	var matchIDs []string
	for _, n := range sessions {
		if n.SymbolName == name {
			matchIDs = append(matchIDs, n.Id)
		}
	}
	if len(matchIDs) > 0 {
		sort.Strings(matchIDs)
		return matchIDs[0], nil
	}
	// Create a new session node. NodeThoughtSession is !Summarizable
	// (neverSummarize, node_type_eligibility.go) and is NOT in the
	// thought/charge summary-validation carve-out (engine_mutate_validate.go:69),
	// so it MUST carry a non-empty Summary or the server rejects the create with
	// "summary is required". The session has no author-supplied summary (only the
	// THOUGHT does); derive a composer summary from the session name — concise,
	// search-optimized, and well within SummaryMaxLen for any reasonable name.
	sessionNode := knowledgev1.Node{
		Type:       string(kgtypes.NodeThoughtSession),
		Source:     "llm:claude",
		SymbolName: name,
		Summary:    thoughtSessionSummary(name),
	}
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{&sessionNode}, nil, "")
	if perr != nil {
		return "", fmt.Errorf("create session: %w", perr)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("create session: no id returned")
	}
	return ids[0], nil
}

// thoughtSessionSummary derives the composer-supplied, search-optimized summary
// for an auto-created NodeThoughtSession from its name. The session has no
// author-supplied summary (only the thought carries the new required param), so
// the composer mints one. truncateAtWordCreate bounds it well under
// validate.SummaryMaxLen even for a pathologically long session name.
func thoughtSessionSummary(name string) string {
	return truncateAtWordCreate("Reasoning session: "+name, validate.SummaryMaxLen)
}

// lastSessionThoughtID returns the ID of the most-recently-created thought
// already in the session (the "prev" the EdgeNext lineage edge originates from),
// or "" when the session has no prior thoughts. Mirrors getSessionThoughts +
// the prev = thoughts[len-2] selection (tools_mutate_create_thought.go:115-119):
// the new thought is NOT yet linked, so the current last session thought IS the
// prev. Reads the session's contained thoughts via an EdgeKGContains traverse
// and picks the latest by CreatedAt.
func lastSessionThoughtID(ctx context.Context, gc GraphCaller, sessionID string) string {
	nodes, err := TraverseDescendants(ctx, gc, sessionID, kgtypes.EdgeKGContains, 1)
	if err != nil || len(nodes) == 0 {
		return ""
	}
	var latest *knowledgev1.Node
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) != kgtypes.NodeThought {
			continue
		}
		if latest == nil || n.CreatedAt > latest.CreatedAt {
			latest = n
		}
	}
	if latest == nil {
		return ""
	}
	return latest.Id
}
