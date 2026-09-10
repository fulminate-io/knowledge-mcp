// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// registration_collector_name_test.go — R3: a registration under a built-in
// COLLECTOR's name is ADMITTED, and the record carries no new field.
//
// THIS PINS TODAY'S BEHAVIOR AS A REQUIREMENT rather than changing it. The
// dispatch now sends such a name to the registration, and the alternative —
// refusing the registration instead — was rejected because it contradicts the
// rule that the registration name IS the graph family, for exactly the eight
// names an operator would want. Pinning it here is what stops a later author
// "fixing" the collision by closing the gate, which would silently remove the
// capability the dispatch change exists to deliver.

// builtinCollectorNames are the names a compiled-in COLLECTOR holds that are NOT
// built-in GRAPH TYPES. They are the collision set: registrable, and shadowing.
var builtinCollectorNames = []string{"aws", "gcp", "azure", "k8s", "cloudwatch", "loki", "stackdriver", "k8s-logs"}

// TestValidateRegistration_AdmitsABuiltinCollectorName is R3. Each of the eight
// family names an operator would register is ADMITTED, with same-run
// known-negatives on both refusal arms so the admissions are evidence of an open
// gate rather than of a validator that accepts everything.
func TestValidateRegistration_AdmitsABuiltinCollectorName(t *testing.T) {
	for _, name := range builtinCollectorNames {
		t.Run("admits "+name, func(t *testing.T) {
			fake := &fakeExec{}
			fake.queueResp(&knowledgev1.ExecuteResponse{Ids: []string{name}})

			require.NoError(t, New(fake).Create(context.Background(), sampleDef(name)),
				"%q is a built-in COLLECTOR name, not a built-in GRAPH TYPE, so it is registrable — the dispatch is what decides which one runs", name)
			require.Len(t, fake.execs, 1, "an accepted registration dispatches exactly one wire call")
		})
	}

	// THE SAME-RUN KNOWN-NEGATIVES, on both refusal arms. Without them, the eight
	// admissions above would be equally true of a validator with no gate at all.
	t.Run("a built-in GRAPH TYPE name is still refused", func(t *testing.T) {
		for _, name := range []string{"code", "practice", "checks"} {
			fake := &fakeExec{}
			err := New(fake).Create(context.Background(), sampleDef(name))
			require.Error(t, err, "%q is a built-in graph type", name)
			assert.Contains(t, err.Error(), "collides with a built-in graph type")
			assert.Contains(t, err.Error(), name, "the refusal names the value it rejected")
			assert.Empty(t, fake.execs, "a rejected registration must not reach the wire")
		}
	})

	t.Run("a RETIRED built-in name is still refused, with its own message", func(t *testing.T) {
		// THREE RETIRED NAMES, not one. cloud and logs joined transformers when
		// the built-in cloud and log collectors were deleted, and each is a name
		// an operator's leftover storage directory still carries — which is the
		// whole reason registration must refuse it rather than adopt it.
		for _, name := range []string{"transformers", "cloud", "logs"} {
			fake := &fakeExec{}
			err := New(fake).Create(context.Background(), sampleDef(name))
			require.Errorf(t, err, "the retired name %q must not register", name)
			assert.Contains(t, err.Error(), "RETIRED")
			assert.NotContains(t, err.Error(), "collides with a built-in",
				"a retired name and a live collision are different answers")
			assert.Empty(t, fake.execs, "a refused registration must not reach the wire")
		}
	})
}

// TestRegistrationRecord_CarriesNoShadowingField is R3's closing clause: nothing
// about the record changed. A record admitted under a colliding name is
// byte-identical to one admitted under a novel name apart from its Name, so
// there is no opt-in field a user must set and none the dispatch could read.
func TestRegistrationRecord_CarriesNoShadowingField(t *testing.T) {
	colliding := sampleDef("gcp")
	novel := sampleDef("jira")

	// Normalize the one field that legitimately differs, then compare the whole
	// record: a new field added for shadowing would show up here whatever it was
	// called, which a named-field assertion could not do.
	colliding.Name = novel.Name
	colliding.Collector.Tool = novel.GetCollector().GetTool()
	colliding.GetCollector().GetStdio().Command = novel.GetCollector().GetStdio().GetCommand()

	assert.Equal(t, novel.String(), colliding.String(),
		"a registration under a colliding name carries no extra field: the precedence is the DISPATCH's rule, not a property of the record")
}
