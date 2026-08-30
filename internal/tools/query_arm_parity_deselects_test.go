// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_parity_deselects_test.go holds the shared `deselecting` sets the
// parity fixtures reference — the per-cell declaration that supplying a param
// routes the call to a DIFFERENT claimant before this arm's gate runs, making the
// cell unreachable (harness header class (h)).
//
// SPLIT FROM query_arm_parity_fixtures_test.go FOR THE LINE BUDGET. The lefthook
// file-length gate globs *.go with only vendor/** and gen/** excluded, so a test
// file hits the identical 500-line commit block a source file does. These two
// helpers are the file's only members that are not part of the fixture table
// itself, which makes them the seam that costs a reader least.

// queryParityThoughtFilterDeselects is the shared `deselecting` set for the three
// arms that bail to the recall surface on a non-empty thought filter
// (hasThoughtQueryFilter, intercept_query_knowledge_search.go). Each of the six
// re-routes the call BEFORE the arm's gate runs, so the cell is unreachable —
// harness header class (h). Probing with a ZERO value instead does not rescue the
// row: accounting counts a key as supplied only when its value is non-empty
// (isEmptyJSONValue), so a zero-valued probe produces neither the re-route nor
// the declared rejection.
func queryParityThoughtFilterDeselects() map[string]bool {
	return map[string]bool{
		"valence_min": true, "valence_max": true,
		"magnitude_min": true, "consistency_max": true,
		"session": true, "connected_to": true,
	}
}

// queryParityPracticeForeignDeselects is the shared `deselecting` set for the
// five practice arms. practiceShapeIsForeign (intercept_query_practice_linkage.go)
// declines a practice payload carrying id or ids BEFORE the entry point resolves
// its stats seam, so the call is handed to the engineDispatch path that serves
// by-id reads and no practice arm's gate ever runs — harness header class (h).
// The cells stay classRejected under the registry's own emptiness-gate rule ("if
// supplying it routes the call to a DIFFERENT arm, the cell is unreachable and
// REJECTED costs nothing"); the row asserts the re-route rather than a rejection
// the arm can no longer emit.
func queryParityPracticeForeignDeselects() map[string]bool {
	return map[string]bool{"id": true, "ids": true}
}
