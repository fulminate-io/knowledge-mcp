// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_retired_logs_params_test.go — THE FOURTEEN LOGS-ONLY COLLECT
// PARAMETERS ARE UNDECLARED, SO THE PRODUCT REFUSES THEM BY NAME.
//
// WHY UNDECLARING IS THE FIX AND NOT A COSMETIC ONE. collect runs
// rejectUndeclaredParams against its own schema (collect.go:46), so a parameter
// that stays DECLARED is accepted, decoded into a struct field nothing reads,
// and reported as success. That is the silent no-op the repository's bad-input
// rule exists to stop, and it is worst for `credential`: an operator could hand
// a secret to a collect that quietly discarded it. Undeclaring the fourteen
// hands them to the gate, which names the offending key.
//
// THE TEST IS THE SCHEMA'S, NOT THE HANDLER'S, for the parameters, and the
// handler's for the refusal — because both halves can fail independently. A
// property could be deleted from the schema while some other arm still swallowed
// the key, and the gate could be unwired while the schema stayed clean.
//
// IT ALSO PINS THE DESCRIPTION, because the description is a shipped contract
// and a generated doc page is derived from it. A collect description that still
// tells an operator to configure a log backend is a live instruction to use a
// route the product no longer has.

// retiredLogsCollectParams are the fourteen parameters the logs collector
// declared. They are listed here rather than derived, deliberately: the schema
// is the thing under test, so deriving the list FROM the schema would make the
// assertion vacuous.
var retiredLogsCollectParams = []string{
	"backend", "provider", "url", "credential", "auth_type", "kube_context",
	"source", "start", "end", "text_filter", "severity_min", "max_entries",
	"filters", "raw_query",
}

func TestCollectSchema_TheRetiredLogsParamsAreUndeclared(t *testing.T) {
	props := CollectToolDef().InputSchema.Properties
	require.NotEmpty(t, props, "control: the collect schema declares parameters at all")

	for _, name := range retiredLogsCollectParams {
		_, declared := props[name]
		assert.Falsef(t, declared,
			"collect still DECLARES %q, so a call carrying it is accepted and silently discarded "+
				"rather than refused by the undeclared-parameter gate", name)
	}

	// THE CONTROL, same map: the parameters that survive are still declared, so
	// this is a statement about these fourteen and not about a schema that lost
	// its properties.
	for _, live := range []string{"type", "id", "force", "params", "seed_urls", "transformer"} {
		_, declared := props[live]
		assert.Truef(t, declared, "control: the surviving parameter %q must still be declared", live)
	}
}

// TestCollectSchema_TheDescriptionNoLongerRoutesToALogBackend pins the prose.
// The description is copied verbatim into the generated tool guide, so a stale
// sentence here ships to a reader as an instruction.
func TestCollectSchema_TheDescriptionNoLongerRoutesToALogBackend(t *testing.T) {
	def := CollectToolDef()
	desc := def.Description
	require.NotEmpty(t, desc, "control: the collect tool has a description")

	for _, dead := range []string{
		"configure_log_backend", // the manage operation R2 removed
		"logs Pipeline",         // the route it named
		`type="logs"`,           // the collector type itself
	} {
		assert.NotContainsf(t, desc, dead,
			"the collect description still names %q, which this release removed", dead)
	}

	// THE CONTROL: the description still documents the routes that survive, so
	// the assertions above are not satisfied by an emptied string.
	assert.Contains(t, desc, `type="web"`, "control: the web route is still described")
	assert.Contains(t, desc, `type="pdf"`, "control: the pdf route is still described")

	// AND THE id PARAM'S OWN SENTENCE, which carried its own logs exemption.
	assert.NotContains(t, def.InputSchema.Properties["id"].Description, `type="logs"`,
		"the id parameter still documents a logs exemption for a type that no longer exists")
}

// TestCollectHandler_ARetiredLogsParamIsRefusedByName is the HANDLER half: the
// gate actually fires on each of the fourteen, with the offending key named.
//
// EVERY ONE IS DRIVEN, not a sample. The fourteen were declared as fourteen
// separate properties and could be deleted one at a time, so a sample would let
// a survivor through.
func TestCollectHandler_ARetiredLogsParamIsRefusedByName(t *testing.T) {
	for _, name := range retiredLogsCollectParams {
		t.Run(name, func(t *testing.T) {
			deps := newCustomDeps(t)
			handled, res := callCollect(deps, `{"type":"code","id":"/tmp/x","`+name+`":"whatever"}`)
			require.True(t, handled, "the collect intercept must claim the call")
			require.Truef(t, res.IsError,
				"collect carrying the retired logs parameter %q must be REFUSED, not accepted and "+
					"discarded", name)
			body := resultText(res)
			assert.Containsf(t, body, name, "and the refusal must name the offending key: %s", body)
		})
	}

	// THE CONTROL, through the same handler: a well-formed collect is NOT refused
	// by the parameter gate. Without it, a gate that rejected every collect would
	// satisfy all fourteen rows above while breaking the tool outright.
	deps := newCustomDeps(t)
	_, res := callCollect(deps, `{"type":"code","id":"/tmp/x"}`)
	if res.IsError {
		assert.NotContains(t, strings.ToLower(resultText(res)), "unknown parameter",
			"control: a well-formed collect must not be refused BY THE PARAMETER GATE — it may "+
				"still fail further down for an unrelated reason, which is not this gate")
	}
}
