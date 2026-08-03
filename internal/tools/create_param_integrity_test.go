// SPDX-License-Identifier: Apache-2.0

package tools

// create_param_integrity_test.go proves two INDEPENDENT param-integrity classes
// across the whole create_* family, against the production intercepts rather
// than against a helper.
//
//  1. TestCreateTools_UndeclaredParamDropped — a top-level key the tool's schema
//     does not declare must be REJECTED, naming the key, instead of silently
//     vanishing. One subtest per tool, so all five are measured rather than three
//     inferred from two probes.
//
//  2. TestCreateTools_DocumentedParamHasNoReader — every param create_ticket and
//     create_project DOCUMENT but no struct field reads reaches no node. These two
//     are CHARACTERIZATION GUARDS, not red-first reproductions: they pass before
//     the retirement (the params already reach no node) and after it (the params no
//     longer exist). Their job is to prove the retirement removes a genuinely dead
//     surface rather than a live one. See each subtest for its own known-positive
//     control, and note that the proof the metadata still LANDS is a separate pair
//     of tests — TestInterceptCreateTicket_Success_StampsBackendMetadata and its
//     create_project sibling — which this file deliberately does not duplicate.
//
//  3. TestCreateTools_SchemaMatchesArgsStruct — every param a tool DECLARES is
//     read by its args struct and vice versa, with no exemption list. That is the
//     standing guard on both classes above: class 1 can only reject what the
//     schema declares, so a param declared and never read would be ACCEPTED and
//     then dropped, and class 2's eleven were exactly that shape.
//
// The shared case table below is what makes all three tests read as one family
// rather than five copies.

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// createInterceptFn is the shape every create_* intercept shares.
type createInterceptFn func(context.Context, ClientDeps, kgtools.CallToolParams) (bool, kgtools.ToolResult)

// createToolCase is one create tool's complete test identity: the wire name, the
// intercept that claims it, its declared schema properties, a zero value of its
// args struct (the parity test reflects over this) and a minimal payload that
// SUCCEEDS against the fixture.
//
// validArgs must succeed on its own, or the undeclared-key subtest proves
// nothing: a call that fails for an unrelated reason is indistinguishable from
// one that fails because the injected key was rejected.
type createToolCase struct {
	tool       string
	intercept  createInterceptFn
	properties map[string]kgtools.Property
	argsStruct any
	validArgs  map[string]any
}

// createToolCases returns a FRESH table on every call so a subtest that injects a
// key into validArgs cannot leak it into a sibling.
func createToolCases() []createToolCase {
	return []createToolCase{
		{
			tool:       "create_research",
			intercept:  InterceptCreateResearch,
			properties: CreateResearchToolDef().InputSchema.Properties,
			argsStruct: createResearchArgs{},
			validArgs: map[string]any{
				"name": "research fixture", "goal": "what this answers", "summary": "research summary",
				"questions": []any{map[string]any{
					"question": "the research question", "summary": "question summary",
				}},
			},
		},
		{
			tool:       "create_test_plan",
			intercept:  InterceptCreateTestPlan,
			properties: CreateTestPlanToolDef().InputSchema.Properties,
			argsStruct: createTestPlanArgs{},
			validArgs: map[string]any{
				"name": "test plan fixture", "goal": "what this verifies", "summary": "test plan summary",
				"steps": []any{map[string]any{
					"name": "step one", "description": "step one description", "summary": "step summary",
				}},
			},
		},
		{
			tool:       "create_plan",
			intercept:  InterceptCreatePlan,
			properties: CreatePlanToolDef().InputSchema.Properties,
			argsStruct: createPlanArgs{},
			validArgs: map[string]any{
				"name": "plan fixture", "goal": "what this achieves", "summary": "plan summary",
				"no_patterns_reason": "trivial",
				"phases": []any{map[string]any{
					"name": "phase one", "summary": "phase summary",
					"steps": []any{map[string]any{
						"name": "step one", "description": "step one description", "summary": "step summary",
					}},
				}},
			},
		},
		{
			tool:       "create_ticket",
			intercept:  InterceptCreateTicket,
			properties: CreateTicketToolDef().InputSchema.Properties,
			argsStruct: createTicketArgs{},
			validArgs: map[string]any{
				"name": "ticket fixture", "project_id": "proj-local", "description": "ticket description",
				"summary": "ticket summary", "no_patterns_reason": "trivial",
			},
		},
		{
			tool:       "create_project",
			intercept:  InterceptCreateProject,
			properties: CreateProjectToolDef().InputSchema.Properties,
			argsStruct: createProjectArgs{},
			validArgs: map[string]any{
				"name": "project fixture", "description": "project description", "summary": "project summary",
			},
		},
	}
}

// createToolFixture wires the package's existing fake harness for a create call:
// a parent project with NO backend metadata (so create_ticket takes its
// local-only path) and a scripted mutate response carrying the created IDs. The
// resolver's Default() is deliberately nil so create_project also runs
// local-only — neither path reaches a remote create.
func createToolFixture(t *testing.T) (interceptTestDeps, *fakeGraphCaller) {
	t.Helper()
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"proj-local": nodeResultJSON(t, "proj-local", "project", map[string]string{}),
		},
		mutateResult: kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["created-1","created-2"]}`}},
		},
	}
	return interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}, fc
}

// createArgsJSON marshals a payload map into the raw Arguments the intercept
// decodes. Building the payload as a map rather than a string literal is what
// lets a subtest inject one extra key into an otherwise-valid set.
func createArgsJSON(t *testing.T, args map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(args)
	require.NoError(t, err)
	return b
}

// TestCreateTools_UndeclaredParamDropped is the family's red-first reproduction:
// a top-level key none of the five schemas declare is supplied alongside an
// otherwise-valid payload, and the call must FAIL naming that key.
//
// ONE PROBE KEY SERVES ALL FIVE. file_paths is the exact key from the live
// incident that motivated the sibling mutate gate, and it is declared as a
// top-level param by none of these five tools — create_plan declares it only
// inside planStepItems(), a NESTED step property, which is why the top-level-only
// scope of the check leaves structured create_plan calls untouched. Each subtest
// re-derives that absence rather than trusting this comment.
func TestCreateTools_UndeclaredParamDropped(t *testing.T) {
	const undeclared = "file_paths"
	for _, tc := range createToolCases() {
		t.Run(tc.tool, func(t *testing.T) {
			// Known-positive control on the probe itself: a key the schema DOES
			// declare would be rejected by nothing, and this case would assert the
			// absence of a behavior that was never in scope.
			_, declared := tc.properties[undeclared]
			require.False(t, declared,
				"%s must not declare %s at top level for this case to mean anything", tc.tool, undeclared)

			// Known-positive control on the FIXTURE: the payload must succeed
			// without the injected key, or a failure below attributes to the
			// injection a rejection the payload earned on its own. This caught a
			// real defect — an earlier one-character step description tripped
			// create_plan's own minimum-length validator, which would have
			// counted as a drop measurement while measuring nothing.
			controlDeps, _ := createToolFixture(t)
			_, controlRes := tc.intercept(opCtx(), controlDeps, kgtools.CallToolParams{
				Name:      tc.tool,
				Arguments: createArgsJSON(t, tc.validArgs),
			})
			require.False(t, controlRes.IsError,
				"the %s fixture must succeed WITHOUT the injected key: %s", tc.tool, toolResultText(controlRes))

			deps, fc := createToolFixture(t)
			args := maps.Clone(tc.validArgs)
			args[undeclared] = "cmd/a.go,cmd/b.go"

			handled, res := tc.intercept(opCtx(), deps, kgtools.CallToolParams{
				Name:      tc.tool,
				Arguments: createArgsJSON(t, args),
			})

			require.True(t, handled, "%s must answer the call here, not pass it down the chain", tc.tool)
			require.True(t, res.IsError,
				"%s must fail rather than succeed with %s silently discarded: %s",
				tc.tool, undeclared, toolResultText(res))
			assert.Contains(t, toolResultText(res), `unknown parameter "`+undeclared+`"`,
				"the error names the param that would otherwise have vanished")
			assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
		})
	}
}

// TestCreateTools_DocumentedParamHasNoReader pins the second class: params both
// schemas publish as caller-suppliable that NO struct field reads. Each is given
// a DISTINCT sentinel — a fixture deriving every value from one string would
// collapse the six into one assertion and stay green even if five of them were
// quietly wired.
//
// HONEST LABEL: these two are guards, not reproductions. They pass today because
// the params reach no node, and they pass after the retirement because the params
// no longer exist (the call is then rejected outright, so there is no node body at
// all). Each therefore carries its own known-positive control proving the fixture
// really does produce an inspectable node body, so a pass can never come from a
// scan that was looking at nothing.
func TestCreateTools_DocumentedParamHasNoReader(t *testing.T) {
	t.Run("ticket_six_params_reach_no_node", func(t *testing.T) {
		sentinels := map[string]any{
			"backend":           "sentinel-ticket-backend",
			"linear_id":         "sentinel-ticket-linear-id",
			"external_url":      "sentinel-ticket-external-url",
			"linear_project_id": "sentinel-ticket-linear-project-id",
			"linear_group_id":   "sentinel-ticket-linear-group-id",
			"linear_group_key":  "sentinel-ticket-linear-group-key",
		}
		base := map[string]any{
			"name": "ticket fixture", "project_id": "proj-local", "description": "ticket description",
			"summary": "ticket summary", "no_patterns_reason": "trivial",
		}
		assertPhantomParamsReachNoNode(t, "create_ticket", InterceptCreateTicket, base, sentinels)
	})

	t.Run("project_five_params_reach_no_node", func(t *testing.T) {
		sentinels := map[string]any{
			"backend":          "sentinel-project-backend",
			"linear_id":        "sentinel-project-linear-id",
			"external_url":     "sentinel-project-external-url",
			"linear_group_id":  "sentinel-project-linear-group-id",
			"linear_group_key": "sentinel-project-linear-group-key",
		}
		base := map[string]any{
			"name": "project fixture", "description": "project description", "summary": "project summary",
		}
		assertPhantomParamsReachNoNode(t, "create_project", InterceptCreateProject, base, sentinels)
	})
}

// assertPhantomParamsReachNoNode runs the guard both ways. The CONTROL run
// submits base alone and requires that it produces a node body the scan can read,
// carrying base's own name — without it, "no sentinel reached a node" would be
// satisfied just as well by a fixture that produced no node bodies at all. The
// PROBE run then adds every phantom param and requires none of their values
// anywhere on any produced node body.
func assertPhantomParamsReachNoNode(
	t *testing.T, tool string, intercept createInterceptFn, base, sentinels map[string]any,
) {
	t.Helper()

	controlDeps, controlFC := createToolFixture(t)
	handled, res := intercept(opCtx(), controlDeps, kgtools.CallToolParams{
		Name: tool, Arguments: createArgsJSON(t, base),
	})
	require.True(t, handled, "%s must claim the control call", tool)
	require.False(t, res.IsError, "the control payload must succeed: %s", toolResultText(res))
	controlBodies := renderedNodeBodies(controlFC)
	require.NotEmpty(t, controlBodies, "the control run must produce an inspectable node body")
	require.Contains(t, strings.Join(controlBodies, "\n"), base["name"],
		"the scan must be able to see a value that DID reach the node — otherwise it reads nothing")

	probeDeps, probeFC := createToolFixture(t)
	args := maps.Clone(base)
	maps.Copy(args, sentinels)
	handled, _ = intercept(opCtx(), probeDeps, kgtools.CallToolParams{
		Name: tool, Arguments: createArgsJSON(t, args),
	})
	require.True(t, handled, "%s must claim the probe call", tool)

	rendered := strings.Join(renderedNodeBodies(probeFC), "\n")
	for param, sentinel := range sentinels {
		assert.NotContains(t, rendered, sentinel,
			"%s: the %s param reached a node — it is not the dead surface this gate retires", tool, param)
	}
}

// TestCreateTools_SchemaMatchesArgsStruct is the standing guard that keeps both
// classes above fixed. The undeclared-key rejection can only reject what the
// schema does not declare, so a param DECLARED and never read still sails through
// and is still dropped — which is precisely what the eleven retired params were.
// Set equality in both directions is what closes that, and it is a materially
// stronger gate than one carrying per-tool exceptions.
//
// NO EXEMPTION LIST, deliberately. If a tool ever needs one, a retirement was
// incomplete and the right response is to finish it, not to record the gap here.
//
// BOTH DIRECTIONS ARE REPORTED SEPARATELY because they are different defects with
// different fixes: declared-but-unread is a phantom param to retire, read-but-
// undeclared is a param the tool consumes that callers cannot legally send — the
// undeclared-key check would reject it. A single "sets differ" message would send
// the next reader looking in the wrong place.
//
// BOTH SIDES ARE RE-DERIVED FROM THE TREE, never asserted against a frozen count,
// so a legitimately added param keeps the test working instead of breaking it.
// For orientation only, the counts at the time this was written are research 6,
// test_plan 5, plan 12, ticket 12 and project 5.
func TestCreateTools_SchemaMatchesArgsStruct(t *testing.T) {
	for _, tc := range createToolCases() {
		t.Run(tc.tool, func(t *testing.T) {
			read := topLevelJSONTags(t, tc.argsStruct)
			require.NotEmpty(t, read, "reflection found no json-tagged fields — the scan is reading nothing")
			require.NotEmpty(t, tc.properties, "the tool declares no properties — the scan is reading nothing")

			var declaredNotRead []string
			for param := range tc.properties {
				if !read[param] {
					declaredNotRead = append(declaredNotRead, param)
				}
			}
			var readNotDeclared []string
			for tag := range read {
				if _, declared := tc.properties[tag]; !declared {
					readNotDeclared = append(readNotDeclared, tag)
				}
			}
			sort.Strings(declaredNotRead)
			sort.Strings(readNotDeclared)

			assert.Empty(t, declaredNotRead,
				"%s DECLARES these params but no args-struct field reads them — a caller supplying one gets a success with it silently dropped; retire them from the schema or wire them",
				tc.tool)
			assert.Empty(t, readNotDeclared,
				"%s READS these params but its schema does not declare them — the undeclared-key check will reject a param the tool actually consumes; declare them",
				tc.tool)
		})
	}
}

// topLevelJSONTags returns the json tag names of v's TOP-LEVEL fields, with any
// ,omitempty suffix stripped. Reflecting over the struct TYPE is what makes this
// honest: a file-wide grep for `json:"` sweeps in the nested sub-object fields
// that create_plan's phases[]/steps[]/criteria[] and create_research's questions[]
// carry, reporting 29 for create_plan against a real 12 — and those nested keys
// are not top-level params at all.
func topLevelJSONTags(t *testing.T, v any) map[string]bool {
	t.Helper()
	typ := reflect.TypeOf(v)
	require.Equal(t, reflect.Struct, typ.Kind(), "the case's argsStruct must be a struct value")
	tags := make(map[string]bool, typ.NumField())
	for field := range typ.Fields() {
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		tags[strings.Split(tag, ",")[0]] = true
	}
	return tags
}

// renderedNodeBodies returns every node body every recorded mutation carried,
// rendered in full. The proto rendering covers metadata AND every scalar field,
// so a sentinel that landed anywhere on a body is visible here — a metadata-only
// scan would miss a value routed into a description or a summary.
func renderedNodeBodies(fc *fakeGraphCaller) []string {
	var rendered []string
	for _, m := range fc.execMutations {
		for _, body := range m.GetNodeBodies() {
			rendered = append(rendered, body.String())
		}
	}
	return rendered
}
