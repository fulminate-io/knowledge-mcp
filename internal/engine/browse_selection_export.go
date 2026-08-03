// SPDX-License-Identifier: Apache-2.0

package engine

// browse_selection_export.go exposes the two compile-local browse primitives a
// CLIENT-SIDE browse arm needs when it builds its own read plan instead of going
// through Compile: the meta-predicate lowering and the browse row cap.
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

import knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

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
