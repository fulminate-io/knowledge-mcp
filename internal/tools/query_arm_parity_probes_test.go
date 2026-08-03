// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_parity_probes_test.go holds the PROBE RULES and the OBSERVATION
// helpers the query parity harness drives with: which value to inject for a
// param, which params no read can ever show, and how a drive's reads and render
// are folded into the one blob a consumed/ignored row is asserted against.
//
// Split from query_arm_parity_test.go (which owns the harness, its four blind
// spots and the precondition classes) purely to keep both files inside the repo's
// file-length convention — the two are one logical unit. The rules here are only
// meaningful against that header: read the BLIND SPOT (2) and BLIND SPOT (4)
// blocks there before adding a member to either exemption set below.

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// querySelectorRoutingParams are the five params BLIND SPOT (4) governs. Their
// rows assert the arm was still selected and behaved; the class comes from the
// resolver table in the registry, never from Target observation.
var querySelectorRoutingParams = map[string]bool{
	"name": true, "repo": true, "account": true, "language": true, "branch": true,
}

// querySelectionOnlyParams are the params whose consumption no captured read can
// distinguish from a drop, on ANY arm. The five selector-routing params are
// members by BLIND SPOT (4); `format` is a member because it selects a RENDER
// PATH rather than landing a value (the same reason mutate's harness exempts it);
// `graph` and `mode` are members because they are the dispatch discriminants for
// nearly every arm. Anything narrower than a whole-surface rule lives on the
// per-arm `opaque` list instead, so a reader can see which arm claimed it.
var querySelectionOnlyParams = func() map[string]bool {
	out := map[string]bool{"format": true, "graph": true, "mode": true}
	maps.Copy(out, querySelectorRoutingParams)
	return out
}()

// The distinctive probe values. Numeric params are probed with an INTEGER
// because several arg structs decode them into int fields (queryReflectArgs.Limit,
// topologyArgs.TopK) where a fractional probe fails to unmarshal and never
// reaches the arm; the value is deliberately implausible so a match in a render
// cannot be a coincidence.
const (
	queryParityNumber     = 4271
	queryParityNumberText = "4271"
)

// queryParityContains folds case before comparing: renderers title-case labels
// and the linkage/practice paths canonicalize graph names, so a case-sensitive
// match would report a correctly-routed param as missing.
func queryParityContains(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// queryParityProbe returns the value to inject for one param and the distinctive
// string a captured read or the render must (or must not) contain. An empty
// distinctive means the row is observed by ARM SELECTION only.
//
// The governing rule, inherited verbatim from the mutate harness: a probe must be
// both ARM-PRESERVING and TYPE-VALID. Type-valid means valid for the param's
// declared JSON type on EVERY struct that decodes it — query decodes queryArgs,
// queryReflectArgs, topologyArgs, simulateClientArgs and the engine's own args, so
// a probe valid for one and not another never reaches the arm and measures nothing.
func queryParityProbe(param string, prop kgtools.Property, fx queryParityFixture) (value any, distinctive string) {
	if v, ok := fx.discriminants[param]; ok {
		return v, ""
	}
	if v, ok := fx.probeValues[param]; ok {
		// A value override keeps the full class assertion; a zero-valued override
		// carries no distinctive, so its row is a behavior-and-class row.
		if s, isString := v.(string); isString && s != "" {
			return v, s
		}
		return v, ""
	}
	switch param {
	case "extra", "meta":
		// map[string]string on queryArgs — a map[string]any probe fails that decode.
		return map[string]any{"probe-" + param + "-key": "probe-" + param}, "probe-" + param
	case "query_vector":
		// Decoded as a base64 string; a non-decodable probe is dropped before the
		// plan is built, which would make a consumed row fail against correct work.
		return queryParityVectorB64, queryParityVectorB64
	}
	switch prop.Type {
	case "boolean":
		return true, ""
	case "number":
		return queryParityNumber, queryParityNumberText
	case "array":
		return []any{"probe-" + param}, "probe-" + param
	case "object":
		return map[string]any{"probe-" + param + "-key": "probe-" + param}, "probe-" + param
	}
	return "probe-" + param, "probe-" + param
}

// queryParityObserved renders everything the drive produced into one searchable
// blob: every captured ExecuteRequest (QueryPlan, Selection and Target), every
// captured StatsRequest, and the RENDERED result text. One observation rule then
// serves the read-issuing arms and the client-rendering arms alike — see
// BLIND SPOT (3) for why the render half is not optional here.
func queryParityObserved(t *testing.T, fc *fakeGraphCaller, res kgtools.ToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, r := range fc.execRequests {
		b, err := json.Marshal(r)
		require.NoError(t, err)
		sb.Write(b)
	}
	for _, r := range fc.statsReqs {
		b, err := json.Marshal(r)
		require.NoError(t, err)
		sb.Write(b)
	}
	sb.WriteString(toolResultText(res))
	return sb.String()
}

// queryParityCompiled compiles the payload the way engine.Dispatch would and
// renders the resulting plan, including its Target. Used ONLY by the gate-only
// arm (armEngineDispatch), whose handler IS that compiler — the substitution
// mutate makes for its declining arms, for the same reason: there is no client
// read to observe, so the consumed assertion is made against the compiled plan.
func queryParityCompiled(t *testing.T, payload []byte) string {
	t.Helper()
	req, ok := engine.Compile("query", json.RawMessage(payload))
	if !ok {
		return ""
	}
	b, err := json.Marshal(req)
	require.NoError(t, err)
	return string(b)
}
