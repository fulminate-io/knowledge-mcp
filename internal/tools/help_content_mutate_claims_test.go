// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHelpMutate_NoUnbackedCapabilityClaims pins the mutate help against
// capability claims that no implementation backs. Each claim below was
// documented for months, was followed by readers, and failed for every one of
// them — the help agreed with itself while disagreeing with the code.
//
// The negative assertions name what must stay gone; the positive ones name
// what must stay present, so a later rewrite cannot satisfy the negatives by
// deleting the surrounding block. Both shipped surfaces are covered: the help
// constant a reader gets from help("mutate"), and the wire schema description
// a caller gets from the tool definition. A fix to one is invisible to the
// other, so a guard over only the constant would leave the schema unprotected.
func TestHelpMutate_NoUnbackedCapabilityClaims(t *testing.T) {
	// The link-target prefix that never resolved: no handler ever created a
	// node from it, in any graph.
	assert.NotContains(t, helpMutate, "file:",
		"helpMutate must not advertise the phantom link-target prefix")

	// The relationship is not checked against any vocabulary — an unrecognized
	// string persists verbatim as a new edge type.
	assert.NotContains(t, helpMutate, "must be a valid edge type",
		"helpMutate must not claim link relationships are validated")

	// No such node type exists in either vocabulary. An unknown type is not
	// rejected on create, but it fails closed on summary and on embed, so a
	// reader who follows the list creates a search-invisible node silently.
	assert.NotContains(t, helpMutate, "observation",
		"helpMutate must not list a node type that does not exist")
	assert.NotContains(t, mutateProperties()["type"].Description, "observation",
		"the mutate wire schema must not list a node type that does not exist")

	// The replacement worked example, in the placeholder form that ships: the
	// reader substitutes a node ID from their own indexed repo.
	assert.Contains(t, helpMutate,
		`mutate({ "operation": "link", "from": "step_id", "to": "path/to/file.go", "relationship": "implements" })`,
		"helpMutate must keep the worked link example")

	// The over-cap summary path clamps and succeeds; it does not raise the
	// error the help used to promise. Losing this warning loses the reader's
	// only notice that content past the cap is discarded.
	assert.Contains(t, helpMutate, "clamped",
		"helpMutate must document that an over-cap summary is clamped, not rejected")
}
