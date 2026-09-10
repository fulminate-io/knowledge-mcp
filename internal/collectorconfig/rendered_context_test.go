// SPDX-License-Identifier: Apache-2.0

package collectorconfig_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// rendered_context_test.go — A SHIPPED COLLECTOR'S RENDERED FOREIGN-GRAPH
// DECLARATION, PUT THROUGH THE VALIDATOR THAT WILL SEE IT.
//
// WHY IT IS HERE AND NOT IN THE MODULE. A collector module cannot import this
// package: it is a separate Go module and this path is client-internal by
// construction. So the module asserts the SHAPE of what it renders, and the
// validator's own package asserts that the same document is one this client
// admits — which is the question a module cannot ask about itself and the one
// that decides whether an operator's file loads at all.
//
// THE COPY IS CHECKED IN, and it is the k8s-logs collector's rendered context
// verbatim (cmd/collectors/k8s-logs/internal/k8slogs/describe.go's
// describedForeignContext, marshaled). The module's own row
// TestDescribedForeignContext_CarriesTheWholeCorrelationSlicePerFamily names the
// same values from the other side, so a render that changed without this file
// changing with it takes two edits in opposite directions rather than one.
//
// WHY THIS COLLECTOR. It is the only shipped module that declares foreign
// context at all after the cloud collectors moved out, it declares the largest
// slice of any of them, and its declaration is the one two tickets have now
// found selecting nothing.

// TestARenderedCollectorContextLoadsAndValidates is row 29a's assertion (b).
func TestARenderedCollectorContextLoadsAndValidates(t *testing.T) {
	raw := renderedContext(t)

	// STEP 1 — it decodes into the client's own declaration type. A shape the
	// loader cannot decode is refused before any validator sees it, which is how
	// an array-shaped context took a whole scoped file down.
	var decl externalcollector.ContextDeclaration
	require.NoError(t, json.Unmarshal(raw, &decl),
		"the collector's rendered context must decode into the type the entry carries")
	require.NotEmpty(t, decl, "the rendered context declares no family at all; every row below would pass vacuously")

	// STEP 2 — it passes the validator the loader runs. This is the half that
	// refuses node fields or metadata keys declared with nothing to carry them,
	// and it is what the all-node-types selector had to satisfy rather than
	// bypass.
	require.NoError(t, decl.Validate(),
		"the collector's rendered context must pass the validator the config loader runs on every load")

	// STEP 3 — it is the SLICE the correlation needs, not merely a valid one. A
	// declaration that selected nothing would also pass Validate.
	for family, family_decl := range decl {
		assert.True(t, family_decl.AllNodeTypes,
			"family %q selects node types by name; three of the four families this collector correlates against emit "+
				"one type called cloud-resource and the fourth emits one per resource kind, so a named list selects nothing", family)
		assert.Contains(t, family_decl.NodeFields, "id",
			"family %q omits the `id` node field, which the edge read is pivoted on", family)
		assert.Subset(t, family_decl.MetadataKeys,
			[]string{"namespace", "cluster_name", "resource_type", "region", "provider"},
			"family %q does not carry the five metadata keys the correlation matches on", family)
		assert.ElementsMatch(t, []string{"from_id", "to_id"}, family_decl.EdgeFields,
			"family %q carries no edge fields; the edges are what CONFIRM a correlation", family)
	}
}

// TestARenderedCollectorContextSurvivesAWholeEntry is the second half: the same
// document inside a whole config entry, through the loader's own refusals rather
// than through the declaration validator alone.
//
// IT IS WHAT CATCHES A `reason` KEY. The declaration validator does not decode
// strictly; the ENTRY decoder does, and a rendered reason is refused there by
// name — the failure that took a scoped file down before ticket 39.
func TestARenderedCollectorContextSurvivesAWholeEntry(t *testing.T) {
	entry := map[string]any{
		"type":    "stdio",
		"command": "/usr/local/bin/knowledge-collector-k8s-logs",
		"tool":    "collect_k8s_logs",
		"context": json.RawMessage(renderedContext(t)),
	}
	file, err := json.Marshal(map[string]any{"collectors": map[string]any{"k8s-logs": entry}})
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), collectorconfig.FileName)
	require.NoError(t, os.WriteFile(path, file, 0o600))

	loaded, found, err := collectorconfig.Loader{UserPath: path}.Resolve("k8s-logs")
	require.NoError(t, err, "the entry a shipped collector's render produces must LOAD")
	require.True(t, found)
	assert.Len(t, loaded.Entry.Context, 4, "and arrive carrying every family the collector declared")

	// THE CONTROL: the same document with a `reason` key added is refused BY
	// NAME. Without it this row could not tell a loader that refuses nothing from
	// one that admits this document deliberately.
	var withReason map[string]map[string]any
	require.NoError(t, json.Unmarshal(renderedContext(t), &withReason))
	for family := range withReason {
		withReason[family]["reason"] = "why this collector needs the slice"
	}
	entry["context"] = withReason
	file, err = json.Marshal(map[string]any{"collectors": map[string]any{"k8s-logs": entry}})
	require.NoError(t, err)
	bad := filepath.Join(t.TempDir(), collectorconfig.FileName)
	require.NoError(t, os.WriteFile(bad, file, 0o600))

	_, _, err = collectorconfig.Loader{UserPath: bad}.Resolve("k8s-logs")
	require.Error(t, err, "a rendered reason must be refused")
	assert.Contains(t, err.Error(), "reason", "and refused BY NAME")
}

// renderedContext reads the checked-in copy of the k8s-logs collector's rendered
// foreign-graph context.
func renderedContext(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "k8s-logs-context.json"))
	require.NoError(t, err, "the checked-in render is what these rows are about")
	return raw
}
