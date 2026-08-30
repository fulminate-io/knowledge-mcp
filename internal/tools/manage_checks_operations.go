// SPDX-License-Identifier: Apache-2.0

// manage_checks_operations.go — the manage_checks tool's operation vocabulary,
// kept beside InterceptManageChecks's dispatch in the same arrangement
// manage_operations.go uses for the manage tool.

package tools

import "slices"

// The operation terms, declared once and cited everywhere. They are constants
// rather than inline literals because the schema enum, the dispatch switch and
// the refusal message all name the same three values, and three hand-typed
// copies is three places for a typo to hide.
const (
	// OpChecksCreate authors a check and both of its fixtures in one call,
	// validating them through the admission gate before anything is written.
	OpChecksCreate = "create"
	// OpChecksList renders the checks-graph inventory.
	OpChecksList = "list"
	// OpChecksRun executes checks against a repo's working tree.
	OpChecksRun = "run"
)

// manageChecksOperations is every operation a `manage_checks` call may name.
// Unlike the manage tool's vocabulary it holds no downstream claimant's
// operations: this tool has exactly one intercept, so the list is its own switch.
//
// Sorted, and sized by construction (len(manageChecksOperations)) — never by a
// hand-written numeral, which is the kind of claim that rots silently.
var manageChecksOperations = []string{
	OpChecksCreate,
	OpChecksList,
	OpChecksRun,
}

// manageChecksOperationKnown reports whether op is one this tool admits.
func manageChecksOperationKnown(op string) bool {
	return slices.Contains(manageChecksOperations, op)
}
