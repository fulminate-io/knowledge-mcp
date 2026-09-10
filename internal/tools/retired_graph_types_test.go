// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/graphtypecrud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// retired_graph_types_test.go — R3: THE cloud, logs AND cicd FAMILY NAMES ARE
// RETIRED, AT EVERY GATE, WITH A CONTROL AT EACH.
//
// WHY RETIREMENT AND NOT DELETION. Deleting the constants alone frees the names:
// IsBuiltinGraphType stops claiming them, so each falls through to the
// registered-custom path, and a user could register a graph type that ADOPTS the
// leftover storage directory an upgrading operator still has on disk. That
// degradation compiles and passes every vocabulary test — it is the failure the
// retirement map exists to make impossible, and it is why the names are recorded
// rather than simply removed.
//
// THE STRUCTURE IS THE TRANSFORMERS REMOVAL'S OWN, not a hand-enumeration: that
// family was retired the same way and its criterion drives the refusal at each
// gate with a per-gate control. This reproduces that shape for the two new names.
//
// EVERY GATE CARRIES A CONTROL IN THE SAME RUN, and the controls differ by gate
// because what would falsify each differs: a surviving builtin where the
// question is "is this name still a family", a novel name where the question is
// "does this gate refuse everything".

// retiredFamilies are every retired family name. cicd is the newest; cloud,
// logs and transformers are the STANDING precedent: a gate that stopped
// honoring the map entirely would still pass rows written only for the new
// name.
var retiredFamilies = []string{"cloud", "logs", "transformers", "cicd"}

// TestRetiredGraphTypes_ThePredicateAnswersForBothAndNotForOthers is gate (1):
// the predicate every other gate reads.
func TestRetiredGraphTypes_ThePredicateAnswersForBothAndNotForOthers(t *testing.T) {
	for _, name := range retiredFamilies {
		reason, retired := kgtypes.RetiredGraphTypeReason(name)
		require.Truef(t, retired, "%q must be recorded as retired", name)
		assert.NotEmptyf(t, reason, "%q owes a removal sentence, not a bare true", name)
		// THE SENTENCE IS ASSERTED AT ITS SOURCE, because every other gate below
		// renders THIS string: pinning it here is what makes the per-gate rows a
		// statement about routing rather than four copies of one check.
		assert.NotContainsf(t, reason, `operation:"register"`,
			"%q's removal sentence sends the reader to custom_collector's register operation, "+
				"which does not exist — the tool's enum is [list] and its own description says "+
				"registration is a config file installed with `knowledge collector add`", name)
	}

	// CONTROL A — the surviving builtins. A predicate broken to answer true for
	// everything would satisfy every row above.
	for _, live := range []string{"knowledge", "code", "practice", "checks"} {
		_, retired := kgtypes.RetiredGraphTypeReason(live)
		assert.Falsef(t, retired, "the surviving builtin %q must NOT report retired", live)
		assert.Truef(t, kgtypes.IsBuiltinGraphType(live), "and it must still be a builtin")
	}

	// CONTROL B — a name that never existed. It is neither builtin nor retired,
	// which is the third state the whole design turns on.
	_, retired := kgtypes.RetiredGraphTypeReason("zzz-never-a-family")
	assert.False(t, retired, "a never-existing name is not a retired one")
	assert.False(t, kgtypes.IsBuiltinGraphType("zzz-never-a-family"))

	// AND THE RETIRED NAMES ARE NOT BUILTINS EITHER. This is what frees them into
	// the registered-custom path, and therefore what makes every other gate
	// necessary rather than redundant.
	for _, name := range retiredFamilies {
		assert.Falsef(t, kgtypes.IsBuiltinGraphType(name),
			"%q must NOT be a builtin — if it were, no other gate here would ever be reached", name)
	}
}

// TestRetiredGraphTypes_RegistrationRefusesTheName is gate (2): the one that
// keeps the freed name unregistrable.
func TestRetiredGraphTypes_RegistrationRefusesTheName(t *testing.T) {
	for _, name := range retiredFamilies {
		err := graphtypecrud.ValidateName(name)
		require.Errorf(t, err, "registering the retired name %q must be refused", name)
		assert.Containsf(t, err.Error(), "RETIRED", "and the refusal must say so, not report a collision: %v", err)
		assert.Contains(t, err.Error(), name, "and name the value it rejected")
	}

	// CONTROL — a novel name still registers, so the gate is a decision about
	// these names rather than a validator that refuses everything.
	assert.NoError(t, graphtypecrud.ValidateName("acme-tracker"),
		"control: a novel family name is still registrable")
}

// TestRetiredGraphTypes_TheSearchRailRefusesWithTheRemovalReason is gate (3).
func TestRetiredGraphTypes_TheSearchRailRefusesWithTheRemovalReason(t *testing.T) {
	deps := &customDeps{crud: registeredCRUD("acme-tracker")}
	for _, name := range retiredFamilies {
		err := validateRegisteredGraphSelector(context.Background(), deps, kgtypes.GraphType(name), "anything")
		require.Errorf(t, err, "the search rail must refuse the retired family %q", name)
		assert.Containsf(t, err.Error(), "retired", "with the removal reason: %v", err)
		assertNamesTheRegistrationRoute(t, "the search rail", err.Error(), name)
	}

	// CONTROL — an unregistered NOVEL name is refused too, but with the OTHER
	// sentence. Without this leg a rail that refused every non-builtin with the
	// retirement text would pass.
	err := validateRegisteredGraphSelector(context.Background(), deps, kgtypes.GraphType("zzz-never"), "anything")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "retired",
		"a never-existing name is an unsupported type, not a retired one")
	assert.Contains(t, err.Error(), "unsupported graph type")
}

// TestRetiredGraphTypes_TheStatsArmRefusesWithTheRemovalReason is gate (4).
func TestRetiredGraphTypes_TheStatsArmRefusesWithTheRemovalReason(t *testing.T) {
	deps := &customDeps{crud: registeredCRUD("acme-tracker")}
	for _, name := range retiredFamilies {
		res, claimed := unknownGraphVocabularyRefusal(context.Background(), deps, name)
		require.Truef(t, claimed, "the stats arm must CLAIM the retired family %q rather than fall through", name)
		assert.True(t, res.IsError)
		assert.Containsf(t, resultText(res), "retired", "with the removal reason: %s", resultText(res))
		assertNamesTheRegistrationRoute(t, "the stats arm", resultText(res), name)
	}

	// CONTROL — a builtin is not claimed by this refusal at all, so the arm is
	// not simply refusing every name it sees.
	_, claimed := unknownGraphVocabularyRefusal(context.Background(), deps, "code")
	assert.False(t, claimed, "control: a surviving builtin is served, not refused")
}

// TestRetiredGraphTypes_TheCollectPathRefusesByName is gate (5), and it is the
// one an operator meets first: their `collect(type:"logs")` still in a script.
func TestRetiredGraphTypes_TheCollectPathRefusesByName(t *testing.T) {
	for _, name := range []string{"cloud", "logs", "cicd"} {
		deps := newCustomDeps(t)
		handled, res := callCollect(deps, `{"type":"`+name+`","id":"anything"}`)
		require.True(t, handled)
		require.Truef(t, res.IsError, "collect(type:%q) must be refused", name)
		assert.Containsf(t, resultText(res), "retired", "with the removal reason: %s", resultText(res))
		// AND THE ROUTE THAT REPLACED IT, NAMED AS IT ACTUALLY EXISTS. The
		// sentence used to send the reader to custom_collector(operation:
		// "register"), an operation the tool does not have: its enum is [list],
		// and its own description says REGISTRATION IS A CONFIG FILE installed
		// with `knowledge collector add`. A refusal naming no route is not
		// actionable; one naming a route that errors is worse, because the reader
		// spends a call finding out.
		assert.Contains(t, resultText(res), "knowledge collector add",
			"the refusal must name the command that actually registers a collector")
		assert.Contains(t, resultText(res), "collectors.json",
			"and the artifact it writes, since the registration IS that file")
		assert.NotContains(t, resultText(res), `operation:"register"`,
			"and it must NOT name custom_collector's register operation, which does not exist")
	}
}

// TestRetiredGraphTypes_TheProviderNamesNameTheContribRoute is R3's other half:
// the seven COLLECTOR names that were compiled in are not retired GRAPH TYPES,
// so they take the unknown-collector path — and that refusal owes the route too.
//
// THE TWO CLASSES ARE DIFFERENT AND BOTH ARE ON THE TICKET. `cloud`, `logs` and
// `cicd` were graph FAMILIES; `aws`, `gcp`, `azure`, `k8s`, `cloudwatch`, `loki`,
// `stackdriver`, `github`, `gitlab` and `bitbucket` were collector NAMES that
// filled them. An operator's script can carry either, and being told "unknown
// collector" with no route is what makes the second class a dead end.
func TestRetiredGraphTypes_TheProviderNamesNameTheContribRoute(t *testing.T) {
	for _, name := range []string{
		"aws", "gcp", "azure", "k8s", "cloudwatch", "loki", "stackdriver",
		"github", "gitlab", "bitbucket",
	} {
		deps := newCustomDeps(t)
		handled, res := callCollect(deps, `{"type":"`+name+`","id":"anything"}`)
		require.True(t, handled)
		require.Truef(t, res.IsError, "collect(type:%q) must be refused — nothing is registered under it", name)
		body := resultText(res)
		assert.Containsf(t, body, name, "the refusal names the type it rejected: %s", body)
		assert.Containsf(t, body, "collector", "and says what kind of thing it looked for: %s", body)
	}

	// CONTROL — a surviving BUILT-IN collector name still resolves in the
	// registry. Without it every row above would hold for a registry that
	// refuses every name.
	_, err := collector.Lookup("web")
	require.NoError(t, err, "control: a compiled-in collector name still resolves")

	// AND THE REFUSAL NAMES THE ROUTE, asserted once on the registry's own
	// message rather than through the collect dispatch: the collect path wraps
	// it in a config-scope refusal whose wording is that path's, while the
	// sentence that owes the contrib route is this one.
	_, missErr := collector.Lookup("aws")
	require.Error(t, missErr)
	assert.Contains(t, missErr.Error(), "CONTRIB collector",
		"the unknown-collector refusal names the route that replaced the built-in")
	assert.Contains(t, missErr.Error(), "aws", "and the names that moved")
	assert.Contains(t, missErr.Error(), "github",
		"and the CI/CD provider names, which moved in this release")
	assert.Contains(t, missErr.Error(), "bitbucket")
}

// TestRetiredGraphTypes_TheManageSurfaceRefusesTheName is gate (6): drop_graph,
// the operation an operator reaches for to clean up a leftover bucket.
func TestRetiredGraphTypes_TheManageSurfaceRefusesTheName(t *testing.T) {
	for _, name := range []string{"cloud", "logs", "cicd"} {
		fc := &fakeGraphCaller{}
		handled, res := dropGraphCall(t, fc,
			`{"operation":"drop_graph","graph":"`+name+`","name":"leftover"}`)
		require.True(t, handled)
		require.Truef(t, res.IsError, "drop_graph on the retired family %q must be refused", name)
		assert.Contains(t, toolResultText(res), "retired")
		assertNamesTheRegistrationRoute(t, "drop_graph", toolResultText(res), name)
		assert.Empty(t, fc.execRequests, "and no mutation may reach the wire")
	}
}

// assertNamesTheRegistrationRoute is the per-gate half of the routing check: the
// refusal an operator actually READS must name the mechanism that exists.
//
// IT IS A HELPER RATHER THAN FOUR COPIES because the four gates render one
// string and what varies is only WHICH gate reached the reader. The transformers
// family is exempt: its removal sentence points at collect's `recipe_body`,
// which is the right route for it and has nothing to do with collectors.
func assertNamesTheRegistrationRoute(t *testing.T, gate, body, family string) {
	t.Helper()
	if family == "transformers" {
		assert.Containsf(t, body, "recipe_body",
			"%s: the transformers removal sentence routes to collect's recipe_body", gate)
		return
	}
	assert.Containsf(t, body, "knowledge collector add",
		"%s renders the %q removal without naming the command that registers a collector: %s",
		gate, family, body)
	assert.Containsf(t, body, "collectors.json",
		"%s renders the %q removal without naming the file the registration IS: %s",
		gate, family, body)
	assert.NotContainsf(t, body, `operation:"register"`,
		"%s sends the reader to a custom_collector operation that does not exist: %s", gate, body)
}
