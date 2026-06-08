// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// renderParamsTable renders tool.InputSchema as a markdown parameter table with
// columns: name / type / required / enum / description. It walks the property
// tree recursively (descending into Property.Properties for object sub-shapes
// and Property.Items for arrays), mirroring the traversal shape of the catalog
// schema guard's walkProperty (cmd/knowledge/internal/tools/catalog_schema_test.go):
// dotted child paths (`path.child`) and a `[]` suffix for array element shapes.
//
// Pure function (schema in, string out). Property keys are iterated in sorted
// order at every depth so the rendered output is byte-stable across runs — Go's
// map iteration is randomized, and the CI drift gate diffs the regenerated tree.
func renderParamsTable(tool kgtools.MCPTool) string {
	required := make(map[string]bool, len(tool.InputSchema.Required))
	for _, r := range tool.InputSchema.Required {
		required[r] = true
	}

	var b strings.Builder
	b.WriteString("| Parameter | Type | Required | Enum | Description |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")

	if len(tool.InputSchema.Properties) == 0 {
		b.WriteString("| _(no parameters)_ | | | | |\n")
		return strings.TrimRight(b.String(), "\n")
	}

	for _, name := range sortedKeys(tool.InputSchema.Properties) {
		writePropertyRows(&b, name, tool.InputSchema.Properties[name], required[name])
	}
	return strings.TrimRight(b.String(), "\n")
}

// writePropertyRows emits one table row for prop at the given dotted path, then
// recurses into nested object properties and array element shapes. path is the
// dotted location shown in the name column; req marks root-level required
// membership (nested keys are not part of the root Required set, so they render
// as not-required).
func writePropertyRows(b *strings.Builder, path string, prop kgtools.Property, req bool) {
	reqMark := ""
	if req {
		reqMark = "yes"
	}
	fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s |\n",
		path,
		mdCell(typeLabel(prop)),
		reqMark,
		mdCell(strings.Join(prop.Enum, ", ")),
		mdCell(descriptionCell(prop)),
	)

	// Recurse into nested object sub-shapes, sorted for determinism.
	for _, childName := range sortedKeys(prop.Properties) {
		writePropertyRows(b, path+"."+childName, prop.Properties[childName], false)
	}
	// Recurse into the array element shape.
	if prop.Items != nil {
		writePropertyRows(b, path+"[]", *prop.Items, false)
	}
}

// typeLabel renders the schema type, annotating arrays with their element type
// when declared (e.g. "array of string", "array of object").
func typeLabel(prop kgtools.Property) string {
	if prop.Type == "array" && prop.Items != nil && prop.Items.Type != "" {
		return "array of " + prop.Items.Type
	}
	if prop.Type == "" {
		return "any"
	}
	return prop.Type
}

// descriptionCell returns the description plus an appended maxLength note when
// the property declares one, so the structural cap surfaces in the table.
func descriptionCell(prop kgtools.Property) string {
	desc := prop.Description
	if prop.MaxLength > 0 {
		if desc != "" {
			desc += " "
		}
		desc += fmt.Sprintf("(max length: %d)", prop.MaxLength)
	}
	return desc
}

// mdCell makes a string safe for a single markdown table cell: escape pipes and
// collapse any embedded newlines into spaces so the row stays on one line.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}

// sortedKeys returns the keys of a Property map in lexical order for
// deterministic, byte-stable rendering.
func sortedKeys(m map[string]kgtools.Property) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
