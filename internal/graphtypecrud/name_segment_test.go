// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// name_segment_test.go — a family name may not contain a COLON, because the
// family name is a SEGMENT of a durable persisted identifier.
//
// WHAT RESTS ON IT. A cross-graph proxy for a node in a custom family is stored
// under "proxy:custom/<family>:<graph>:<node id>", and the id is persisted, so
// the damage outlives the registration.
//
// ONLY THE FAMILY IS CONSTRAINED, and the reason is reachability rather than
// position. A NODE ID may carry colons freely and routinely does — a code node
// id is full of them — because it is the last segment. A GRAPH NAME carrying
// one is not so much harmless as UNCHANGED IN RISK: (graph "a:b", node "c")
// and (graph "a", node "b:c") already render the same id on every arm,
// including the four builtin ones, so refusing it here would fix nothing that
// the builtin arms do not equally have. The FAMILY is different: it is the one
// segment an operator names at admission, before any id exists, and it is the
// only one this predicate can reach. Once a family is registered, every id
// derived from it is already wrong and refusing later refuses the operator's
// own data.
//
// A SLASH STAYS ADMITTED and that is deliberate, so a later reader does not
// tighten it by guess: the generic proxy id's second segment carries a slash in
// every case, so a family name containing one changes nothing about the id.

func TestValidateName_RefusesAColonAndAdmitsASlash(t *testing.T) {
	for _, name := range []string{"acme:tracker", ":leading", "trailing:", "a:b:c"} {
		t.Run("refuses "+name, func(t *testing.T) {
			err := ValidateName(name)
			require.Error(t, err, "a family name carrying a colon breaks the proxy id's segment count")
			assert.Contains(t, err.Error(), name, "the refusal names the value it rejected")
			assert.Contains(t, err.Error(), ":", "the refusal names the character")
		})
	}

	// THE SAME-RUN CONTROLS. Without them the refusals above are equally true of
	// a predicate that rejects everything.
	for _, name := range []string{"acme-tracker", "acme/tracker", "jira", "acme.tracker", "acme_tracker"} {
		t.Run("admits "+name, func(t *testing.T) {
			assert.NoError(t, ValidateName(name),
				"%q carries no colon and must stay registrable; a slash in particular is harmless to the id", name)
		})
	}
}
