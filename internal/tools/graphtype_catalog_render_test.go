// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// graphtype_catalog_render_test.go pins the catalog half of the opt-in default's
// stated observable: a CLI-default family must PRINT summarizable=false and
// embeddable=false, not a dash.
//
// THE DIFFERENCE BETWEEN false AND A DASH IS THE WHOLE ROW. triBool renders a
// value only when the pointer is SET, so an unset flag prints "-" meaning "nobody
// said" while an explicit false prints "false" meaning "this family opted out".
// Both reach the same server behavior, and an operator reading the catalog can
// only tell them apart here. That is why the loader writes the two booleans
// explicitly rather than leaving them out: if it stopped, the behavior would not
// change and only this render would, silently.

func behaviorRow(t *testing.T, table, name string) string {
	t.Helper()
	for line := range strings.SplitSeq(table, "\n") {
		if strings.HasPrefix(line, "| "+name+" ") {
			return line
		}
	}
	t.Fatalf("no row for %q in:\n%s", name, table)
	return ""
}

// TestFormatGraphTypesTable_PrintsAnExplicitFalseRatherThanADash is R7's catalog
// observable.
func TestFormatGraphTypesTable_PrintsAnExplicitFalseRatherThanADash(t *testing.T) {
	no, yes := false, true

	table := formatGraphTypesTable([]*knowledgev1.GraphTypeDef{
		{
			// THE CLI DEFAULT: syncable set true, the two LLM axes set FALSE, which
			// is exactly what the loader writes for an entry with no behavior block.
			Name:     "cli-default",
			Behavior: &knowledgev1.BehaviorDefaults{Syncable: &yes, Summarizable: &no, Embeddable: &no},
		},
		{
			// THE CONTROL: nothing set at all. It must render dashes, or "opted out"
			// and "nobody said" would be indistinguishable and the row above would
			// prove nothing about what the loader wrote.
			Name:     "nothing-declared",
			Behavior: &knowledgev1.BehaviorDefaults{},
		},
		{
			// AND AN OPTED-IN FAMILY, so the row is not satisfied by a render that
			// prints false for everything.
			Name:     "opted-in",
			Behavior: &knowledgev1.BehaviorDefaults{Syncable: &yes, Summarizable: &yes, Embeddable: &yes},
		},
	})

	cliDefault := behaviorRow(t, table, "cli-default")
	assert.Contains(t, cliDefault, "| true | false | false |",
		"a CLI-default family must print syncable true and BOTH LLM axes false: the operator's "+
			"catalog is where an explicit opt-out is distinguishable from nobody having said")

	control := behaviorRow(t, table, "nothing-declared")
	assert.Contains(t, control, "| - | - | - |",
		"CONTROL: an unset flag renders a dash. Without this arm the assertion above would pass "+
			"against a render that printed false for an unset pointer, which is the one thing "+
			"triBool exists to prevent")

	optedIn := behaviorRow(t, table, "opted-in")
	assert.Contains(t, optedIn, "| true | true | true |",
		"CONTROL: an opted-in family prints true, so the row above is not satisfied by a render "+
			"that prints false unconditionally")
}

// TestFormatGraphTypesTable_OverrideCountIsPerNodeTypeOnly pins the column the
// ticket's correction withdrew from R4's observable, so nobody widens it to make
// a sentence true: it counts per-node-type overrides and a GRAPH-level field list
// does not move it.
func TestFormatGraphTypesTable_OverrideCountIsPerNodeTypeOnly(t *testing.T) {
	yes := true
	table := formatGraphTypesTable([]*knowledgev1.GraphTypeDef{{
		Name: "lists-but-no-overrides",
		Behavior: &knowledgev1.BehaviorDefaults{
			Embeddable:      &yes,
			EmbedFields:     []string{"summary", "content"},
			SummarizeFields: []string{"content"},
			Bm25Fields:      []string{"symbol_name"},
		},
	}})
	row := behaviorRow(t, table, "lists-but-no-overrides")
	require.True(t, strings.HasSuffix(strings.TrimSpace(row), "| 0 |"),
		"three graph-level field lists must leave the override count at 0: the column counts "+
			"per-node-type overrides, which is why R4's observable is `collector get` and not "+
			"this column. Row: %s", row)
}
