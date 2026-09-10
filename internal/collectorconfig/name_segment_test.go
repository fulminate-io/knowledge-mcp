// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// name_segment_test.go — the CONFIG-FILE admission route reaches the same
// family-name predicate the register/upsert route and the CLI do.
//
// WHY THIS ROUTE NEEDS ITS OWN TEST. There are three ways a family name is
// admitted: `knowledge collector add`, the graph-type upsert, and a HAND-EDITED
// config file loaded here. The first two share one predicate. This one used to
// share nothing with them — the package imported nothing from the validator at
// all — so a name refused by both of the others was admitted here, on the route
// an operator writing a collector by hand is most likely to take. A shape rule
// enforced on two of three admission routes is not enforced.

func TestValidateEntry_RefusesAColonInTheFamilyName(t *testing.T) {
	entry := Entry{Type: TransportStdio, Command: "/p", Tool: "collect_graph"}

	for _, name := range []string{"acme:tracker", "a:b:c"} {
		t.Run("refuses "+name, func(t *testing.T) {
			err := validateEntry("/cfg/collectors.json", name, entry)
			require.Error(t, err, "the entry name IS the graph family, so it takes the family's shape rules")
			assert.Contains(t, err.Error(), name, "the refusal names the value it rejected")
			assert.Contains(t, err.Error(), "/cfg/collectors.json", "and the file the operator has to edit")
		})
	}

	// THE SAME-RUN CONTROLS: a name with no colon still loads, and the two rules
	// this route already had still fire.
	t.Run("admits an ordinary family name", func(t *testing.T) {
		assert.NoError(t, validateEntry("/cfg/collectors.json", "acme-tracker", entry))
	})
	t.Run("admits a slash, which the proxy id tolerates", func(t *testing.T) {
		assert.NoError(t, validateEntry("/cfg/collectors.json", "acme/tracker", entry))
	})
	t.Run("still refuses an empty name", func(t *testing.T) {
		err := validateEntry("/cfg/collectors.json", "  ", entry)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty name")
	})

	// THE DELIBERATE NON-REFUSAL, pinned so a later author does not "finish the
	// job" by routing the whole of ValidateName through here. This loader takes
	// the SHAPE half of the family-name rules and not the CLAIM half. A built-in
	// graph type's name is admitted BY THIS LOADER on purpose: the collect
	// dispatch short-circuits such a name before it reads any registration, and
	// the test that proves that gate independently of any write path builds its
	// state by writing exactly this entry into a config file. Refusing it here
	// would make that state unconstructible.
	t.Run("admits a built-in graph type name, which the collect dispatch refuses instead", func(t *testing.T) {
		assert.NoError(t, validateEntry("/cfg/collectors.json", "logs", entry),
			"the loader answers the SHAPE question only; whether a name may be CLAIMED is the dispatch's and the write path's")
	})
}
