// SPDX-License-Identifier: Apache-2.0

package engine

// browse_selection_export.go exposes the compile-local and render-local browse
// primitives a CLIENT-SIDE browse arm needs when it builds its own read plan
// instead of going through Compile: the meta-predicate lowering, the browse row
// cap, and the browse renderer itself.
//
// WHY SHIMS RATHER THAN EXPORTS OF THE ORIGINALS. Both symbols live in
// compile_query.go, which the rules-paging work is fenced OUT of: renaming
// browseDefaultLimit there cascades into compile_query_test.go, which references
// the unexported name, and widening browseSelection's signature is impossible
// anyway — it takes []fieldPredicateArg, an unexported type, so no amount of
// exporting makes the full signature reachable from package tools. A shim beside
// the definition keeps ONE definition of each behavior while leaving the
// definition's file untouched.
//
// A SECOND COPY OF EITHER WOULD BE A SILENT DEFECT, not a style problem. A
// re-implemented meta lowering that mapped "*" to equality-against-the-literal-
// asterisk instead of OP_EXISTS would compile, pass a key-only assertion, and
// return nothing in production; a hardcoded 10 in the tools package would drift
// away from the engine's cap the first time the cap moved.

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// BrowseDefaultLimit is the exported alias of the browse row cap
// applyBrowseLimitOffset applies (compile_query.go). A client-side arm that
// builds its own plan bypasses that helper and therefore owns the cap itself;
// reading it from here is what keeps the two spellings of "how a browse limit
// defaults" the same number.
const BrowseDefaultLimit = browseDefaultLimit

// LowerMetaPredicates is the exported wrapper over lowerMetaPredicates: the meta
// equality map lowered onto the proto vocabulary, with "*" as the key-presence
// sentinel (OP_EXISTS) and every other value an exact match (OP_EQ). Returns nil
// for an empty map.
func LowerMetaPredicates(meta map[string]string) []*knowledgev1.MetadataPredicate {
	return lowerMetaPredicates(meta)
}

// BrowseCtx is the exported form of browseContext (render_misc.go), carrying the
// render inputs a client-side browse arm derives from its query args.
type BrowseCtx struct {
	Label    string
	NodeType string
	Offset   int
	Format   string
	Fields   []string
	MetaKeys []string // the meta filter keys, surfaced inline per node.
	// IncludeTombstones mirrors browseContext's own field and must stay in step
	// with it: RenderBrowse converts between the two structs, so a field present
	// on one and absent from the other fails to compile.
	IncludeTombstones bool
}

// RenderBrowse is the exported wrapper over renderBrowseResponse: the numbered
// markdown list with status, ID, truncated description, inline meta values and
// the pagination footer, or the {graph, type, results, total} JSON payload when
// Format is "json". It DELEGATES rather than duplicating — a second copy of the
// render would drift from the pagination footer and the fields projection the
// original owns, both of which callers depend on.
func RenderBrowse(resp *knowledgev1.ExecuteResponse, c BrowseCtx) (kgtools.ToolResult, error) {
	return renderBrowseResponse(resp, browseContext(c))
}
