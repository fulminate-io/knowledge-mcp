// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// browse_drain.go holds the shared id-KEYSET browse drain: the cursor core in its
// two page shapes (hydrated nodes and ids-only). It lives in package engine
// because every bounded whole-type read in the client needs it — the thought
// graph's type browses and adjacency read, the file_symbols/ast file index, the
// topology type fetch, and the graph-wide node enumeration — and engine is the
// one package all of those already import.

// BrowsePageSize is the per-page row count for the keyset browse drain. 500 is a
// low RPC count with a bounded per-page payload. A POSITIVE limit is also what
// bypasses the compiler's limit<=0 -> browseDefaultLimit(10) rewrite in
// applyBrowseLimitOffset (compile_query.go), which is exactly the silent cap the
// drain exists to defeat.
const BrowsePageSize = 500

// DrainKeysetPages is the shared id-KEYSET drain core: it repeatedly invokes
// fetchPage(afterID) for an unknown-length type-browse, advancing the cursor to
// the LAST id of each page, and terminates on the first short or empty page.
// Page 1 passes the EMPTY cursor, which the callers must SET on the plan rather
// than omit — presence is what selects the keyset browse, and an omitted field
// would page in the backend's default order (CreatedAt-desc locally), making the
// cursor taken from page 1 skip every lower id.
//
// The cursor is an id rather than an offset because an OFFSET page must scan and
// discard `offset` rows before returning any, so a full drain costs quadratically
// in corpus size; a keyset page is an index seek that early-terminates at ~limit
// rows, making the drain linear.
//
// The seen-set is kept as a cheap INVARIANT GUARD, not as the correctness
// mechanism it was under offset paging: a keyset cursor is anchored to a row's
// id, not to a position, so a mid-drain insert can no longer shift a page
// boundary and re-emit a row. If the set ever drops something now, a backend
// returned a row at or before the cursor — a real bug, silently absorbed here as
// it was before.
//
// Serial by necessity: page N+1's cursor is page N's last id, so the drain cannot
// be parallelized over an unknown total.
func DrainKeysetPages(fetchPage func(afterID string) ([]*knowledgev1.Node, error), pageSize int) ([]*knowledgev1.Node, error) {
	var out []*knowledgev1.Node
	err := DrainKeysetPagesFunc(fetchPage, pageSize, func(page []*knowledgev1.Node) error {
		out = append(out, page...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DrainKeysetPagesFunc is the STREAMING shape of the same cursor core: it hands
// each page to onPage instead of accumulating, so a caller that only needs to
// FOLD over the corpus (a counter, a matrix accumulator, a bounded top-K) never
// holds the whole node set in memory. DrainKeysetPages above is the accumulating
// wrapper over it — one core, two shapes, the arrangement DrainKeysetIDs already
// shares with it below.
//
// onPage receives only the rows the seen-set has not already yielded, and an
// error it returns aborts the drain. Every paragraph of DrainKeysetPages'
// rationale applies verbatim.
func DrainKeysetPagesFunc(fetchPage func(afterID string) ([]*knowledgev1.Node, error), pageSize int, onPage func([]*knowledgev1.Node) error) error {
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := fetchPage(cursor)
		if err != nil {
			return err
		}
		fresh := make([]*knowledgev1.Node, 0, len(page))
		for _, n := range page {
			if seen[n.Id] {
				continue
			}
			seen[n.Id] = true
			fresh = append(fresh, n)
		}
		if len(fresh) > 0 {
			if err := onPage(fresh); err != nil {
				return err
			}
		}
		if len(page) < pageSize {
			break // short (or empty) final page — corpus exhausted.
		}
		cursor = page[len(page)-1].GetId()
	}
	return nil
}

// DrainKeysetIDs is the RETURN_MODE_IDS twin of DrainKeysetPages: the same cursor
// core over a page of bare ids. An ids-mode response carries no Node structs, so
// DrainKeysetPages cannot serve it; the cursor advances to page[len(page)-1]
// directly rather than through GetId().
//
// Every paragraph of DrainKeysetPages' rationale applies verbatim — page 1 must
// SET the empty cursor rather than omit it, the keyset cursor keeps the drain
// linear where an offset cursor would be quadratic, and the seen-set is an
// invariant guard rather than the correctness mechanism.
// EdgePivotPageSize is the pivot-id count per bounded edges read.
const EdgePivotPageSize = 500

// edgeDedupKey identifies an edge for the pivot drain's union. The pivot SET
// form yields the union over EACH pivot with no cross-pivot dedup, so an edge
// whose two endpoints both sit in the pivot set arrives twice within one page,
// and an edge straddling two pages arrives once per page.
type edgeDedupKey struct {
	fromID   string
	toID     string
	edgeType string
}

// DrainPivotEdges is the EDGE sibling of the keyset drains above: it reads every
// edge incident to a pivot id set in bounded pages instead of one match-all
// request. ids is chunked into consecutive pages of pageSize pivots (pageSize<=0
// means EdgePivotPageSize), fetchPage is called once per chunk, and the yielded
// edges are deduped into a single complete union. An EMPTY id set returns nil
// without calling fetchPage at all, matching the empty-graph shape of the
// match-all readers this replaces.
//
// PER-PAGE TRUNCATION IS DETECTED, NOT ASSUMED COMPLETE. edgeCap is the per-page
// edge ceiling the caller set on its plan's Limit; a page coming back with at
// least that many edges may have been cut off server-side, so the drain refuses
// to accept it. `>=` rather than `==` keeps a server that over-delivers honest,
// and a page holding exactly edgeCap edges with nothing dropped is treated as
// truncated — the safe direction, since the cost is one extra split rather than
// a silently short union. edgeCap<=0 disables detection.
//
// A saturated page HALVES: a smaller pivot set selects strictly fewer edges, so
// each half is retried independently until the pages come in under the cap.
// Recursion bottoms out at a single pivot — if one pivot alone still saturates,
// no further split exists, and the drain returns an error naming that pivot
// rather than a short union. A node with more edges than the cap cannot be
// served completely by this primitive, and its caller must learn that instead of
// ranking a silent sample.
//
// Serial by necessity: the dedup map is shared across pages and the round trips
// are the cost, not CPU.
func DrainPivotEdges(ids []string, pageSize, edgeCap int, fetchPage func(idPage []string) ([]knowledgev1.Edge, error)) ([]knowledgev1.Edge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if pageSize <= 0 {
		pageSize = EdgePivotPageSize
	}
	seen := make(map[edgeDedupKey]bool, len(ids))
	out := make([]knowledgev1.Edge, 0, len(ids))
	for start := 0; start < len(ids); start += pageSize {
		end := min(start+pageSize, len(ids))
		if err := drainPivotPage(ids[start:end], edgeCap, fetchPage, seen, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// drainPivotPage reads ONE pivot page into the shared union, halving the page
// and retrying when the server's answer comes back at the per-page edge cap.
func drainPivotPage(
	pivots []string,
	edgeCap int,
	fetchPage func(idPage []string) ([]knowledgev1.Edge, error),
	seen map[edgeDedupKey]bool,
	out *[]knowledgev1.Edge,
) error {
	edges, err := fetchPage(pivots)
	if err != nil {
		return err
	}
	if edgeCap > 0 && len(edges) >= edgeCap {
		if len(pivots) == 1 {
			return fmt.Errorf(
				"engine: pivot %q alone returns at least %d edges, the per-page ceiling — its edge set cannot be read completely by a pivot drain",
				pivots[0], edgeCap)
		}
		mid := len(pivots) / 2
		if err := drainPivotPage(pivots[:mid], edgeCap, fetchPage, seen, out); err != nil {
			return err
		}
		return drainPivotPage(pivots[mid:], edgeCap, fetchPage, seen, out)
	}
	for i := range edges {
		e := &edges[i]
		key := edgeDedupKey{fromID: e.FromId, toID: e.ToId, edgeType: e.Type}
		if seen[key] {
			continue
		}
		seen[key] = true
		// Built field-by-field into append: knowledgev1.Edge embeds a proto
		// MessageState, so returning or copying one by value trips copylocks.
		*out = append(*out, knowledgev1.Edge{
			FromId:        e.FromId,
			ToId:          e.ToId,
			Type:          e.Type,
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: e.LastValidated,
		})
	}
	return nil
}

func DrainKeysetIDs(fetchPage func(afterID string) ([]string, error), pageSize int) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := fetchPage(cursor)
		if err != nil {
			return nil, err
		}
		for _, id := range page {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
		if len(page) < pageSize {
			break // short (or empty) final page — corpus exhausted.
		}
		cursor = page[len(page)-1]
	}
	return out, nil
}
