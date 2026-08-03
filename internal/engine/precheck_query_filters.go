// SPDX-License-Identifier: Apache-2.0

package engine

import "fmt"

// precheck_query_filters.go holds the by-id filter refusal. It is a sibling of
// compile_query.go (which owns precheckQuery itself) purely to keep both files
// inside the repo's file-length convention — the same reason the mutate arm
// registry was split off its accounting sibling.

// idSelectorRefusedParams is the fixed, SORTED list of params a by-id read
// cannot apply. Iterating it in order makes the reported first hit
// deterministic: the same payload always names the same param, mirroring the
// precomputed sorted ordering the write-side accounting gate uses.
//
// `status` is deliberately ABSENT, and the reason is narrower than "status never
// reaches the engine". For graph ""/knowledge an id-bearing query with no text is
// claimed upstream by the reflect arm, whose status-bearing shape routes to
// recall; for the other builtin graphs an id read is claimed unconditionally by
// that graph's own arm before dispatch. Adding status here would fire on the
// working knowledge recall shape — trading a narrow residual leak (a custom
// registered graph, which has only a text-search claim, drops status on an
// id read) for a broad false rejection.
var idSelectorRefusedParams = []string{"meta", "text", "type", "types"}

// refuseFiltersAlongsideIDSelector reports a validation error when a filter or
// search term rides along with an id-selector.
//
// The mode-less compile precedence is ids → id → text → types → type: when id or
// ids is present the plan is built by the ids or id arm and RETURNS, so every
// filter arm below is unreachable. Without this refusal the caller supplies a
// filter, receives a successful non-empty answer, and gets no signal the filter
// was never applied — the over-broad twin of a silently dropped filter returning
// zero rows.
//
// A by-id read is a LOOKUP, not a browse, so the disposition is refuse rather
// than honor: honoring a type filter on a lookup would be a new capability, and
// honoring a text term on one is incoherent — the caller asked for a lookup and
// a search at once. This is the read-side twin of the write-side rule that an
// arm must consume a supplied param or reject it naming the field.
//
// Not graph-gated, deliberately: no compile path applies a filter to a by-id
// read on any graph, and the graph-specific id reads are claimed upstream before
// dispatch, so they never reach this seam.
func refuseFiltersAlongsideIDSelector(a queryArgs) error {
	if a.ID == "" && len(a.IDs) == 0 {
		return nil
	}
	for _, param := range idSelectorRefusedParams {
		supplied := false
		switch param {
		case "meta":
			supplied = len(a.Meta) > 0
		case "text":
			supplied = a.Text != ""
		case "type":
			supplied = a.Type != ""
		case "types":
			supplied = len(a.Types) > 0
		}
		if !supplied {
			continue
		}
		return validationError(fmt.Sprintf(
			"query(%s): %s is not applied by a by-id read — the ids and id arms claim the call "+
				"ahead of every filter arm; drop it, or issue a search or browse without id/ids",
			idSelectorLabel(a), param,
		))
	}
	return nil
}

// idSelectorLabel names which selector the caller actually sent, so the error
// quotes the payload back rather than a generic placeholder.
func idSelectorLabel(a queryArgs) string {
	if a.ID != "" {
		return "id"
	}
	return "ids"
}
