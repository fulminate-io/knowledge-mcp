// SPDX-License-Identifier: Apache-2.0

package engine

import (
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
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := fetchPage(cursor)
		if err != nil {
			return nil, err
		}
		for _, n := range page {
			if seen[n.Id] {
				continue
			}
			seen[n.Id] = true
			out = append(out, n)
		}
		if len(page) < pageSize {
			break // short (or empty) final page — corpus exhausted.
		}
		cursor = page[len(page)-1].GetId()
	}
	return out, nil
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
