// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusLockdownComment is the verbatim two-line block asserting the status
// field is an OPEN STRING, not a closed enum. It appears in kgtypes (client)
// and store/graph_types_vocab.go (server). The client copy is guarded here; the
// server copy by a sibling assertion in the server module.
const statusLockdownComment = "// Status values for work nodes. Internal vocabulary, NOT a closed enum —\n" +
	"// see mutateRequestArgs.Status comment for the open-string contract."

// TestStatusOpenStringLockdown_Untouched proves the per-type-param update
// rejection did NOT close the status enum: the status schema property carries no
// Enum (rejection is param-PRESENCE-per-type, never status-VALUE validation),
// and the client-side open-string lockdown comment block is present verbatim. A
// future edit that adds a status Enum or deletes the lockdown comment fails here.
func TestStatusOpenStringLockdown_Untouched(t *testing.T) {
	status, ok := mutateProperties()["status"]
	require.True(t, ok, "status must be a declared mutate param")
	assert.Empty(t, status.Enum, "status must stay an OPEN STRING — no closed-enum validation")

	src, err := os.ReadFile("../kgtypes/graph_types.go")
	require.NoError(t, err)
	assert.Contains(t, string(src), statusLockdownComment,
		"the kgtypes status open-string lockdown comment must be present verbatim")
}

// TestMutateParamAccounting_TablePartitionsSchemaPerArm is the schema↔handler
// parity contract, now asserted PER ARM rather than once for the whole update
// path. It replaces a single static classification map that stayed green while
// individual arms dropped params: a one-map-for-all-arms classification cannot
// see that one arm consumes a param another silently discards.
//
// For every arm it asserts the three sets PARTITION the live schema exactly:
//
//	(a) their union is the exact key set of mutateProperties() — a schema param
//	    in no set is unclassified and fails here until someone classifies it;
//	(b) the three sets are pairwise disjoint — no param may be two things at once;
//	(c) no set names a key absent from the live schema — the stale-entry drift
//	    guard, so a removed or renamed param cannot linger in the table.
func TestMutateParamAccounting_TablePartitionsSchemaPerArm(t *testing.T) {
	schema := mutateProperties()
	require.NotEmpty(t, schema, "mutateProperties() must declare params")
	require.NotEmpty(t, mutateArmRegistry, "the arm registry must declare arms")

	for arm, spec := range mutateArmRegistry {
		t.Run(string(arm), func(t *testing.T) {
			// (b) pairwise disjoint.
			for key := range spec.consumed {
				assert.NotContainsf(t, spec.rejected, key,
					"param %q is BOTH consumed and rejected for arm %q", key, arm)
				assert.NotContainsf(t, spec.deliberatelyIgnored, key,
					"param %q is BOTH consumed and deliberately ignored for arm %q", key, arm)
			}
			for key := range spec.rejected {
				assert.NotContainsf(t, spec.deliberatelyIgnored, key,
					"param %q is BOTH rejected and deliberately ignored for arm %q", key, arm)
			}

			// (a) every live schema key is classified.
			for key := range schema {
				_, classified := paramClassFor(arm, key)
				assert.Truef(t, classified,
					"schema param %q is unclassified for arm %q — consume it, reject it, or deliberately ignore it with a justification",
					key, arm)
			}

			// (c) no set names a key absent from the live schema.
			for key := range spec.consumed {
				assert.Containsf(t, schema, key,
					"arm %q names consumed param %q which is absent from mutateProperties() — stale entry (schema drift)", arm, key)
			}
			for key := range spec.rejected {
				assert.Containsf(t, schema, key,
					"arm %q names rejected param %q which is absent from mutateProperties() — stale entry (schema drift)", arm, key)
			}
			for key := range spec.deliberatelyIgnored {
				assert.Containsf(t, schema, key,
					"arm %q names deliberately-ignored param %q which is absent from mutateProperties() — stale entry (schema drift)", arm, key)
			}

			// Every deliberately-ignored entry must carry its justification: the
			// class exists to record a reasoned drop, never to park a param.
			for key, justification := range spec.deliberatelyIgnored {
				assert.NotEmptyf(t, justification,
					"arm %q ignores param %q with no justification", arm, key)
			}
		})
	}
}
