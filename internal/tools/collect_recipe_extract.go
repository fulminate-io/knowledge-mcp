// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// collect_recipe_extract.go holds the collect tool's EXTRACT-mode surface: the
// refusal that keeps the four extract params from being silently ignored, and
// the renderer that applies the byte cap.
//
// THE BYTE CAP LIVES HERE AND NOWHERE ELSE. Only the renderer knows how large a
// rendered row is, so the recipe package cannot apply it; a MaxBytes field on
// recipe.Options would be declared and never read, and a caller setting it
// would be silently ignored while the result claimed both caps had run.

// rejectRecipeOnlyArgs refuses the extract params wherever they would not be
// consumed, naming the offending one.
//
// A param that is declared, accepted and then dropped is worse than one that
// does not exist: the caller reads a successful response and believes the knob
// took effect. This says so instead.
//
// It deliberately does NOT close the pre-existing gap where transformer, recipe
// and dry_run are silently ignored for non-web/pdf types — that behaviour is
// left byte-identical, because widening it is a separate decision.
func rejectRecipeOnlyArgs(a collectArgs) error {
	set := map[string]bool{
		"extract":     a.Extract,
		"recipe_body": a.RecipeBody != "",
		"max_rows":    a.MaxRows != 0,
		"max_bytes":   a.MaxBytes != 0,
	}
	var supplied []string
	for name, on := range set {
		if on {
			supplied = append(supplied, name)
		}
	}
	if len(supplied) == 0 {
		return nil
	}
	sort.Strings(supplied) // deterministic message
	if a.Type != "web" && a.Type != "pdf" {
		return fmt.Errorf("collect %s: %s %s only supported for type=web or type=pdf",
			a.Type, strings.Join(supplied, "/"), pluralIs(len(supplied)))
	}
	if a.Transformer != "recipe" {
		return fmt.Errorf("collect %s: %s %s only supported with transformer=\"recipe\" (got transformer=%q)",
			a.Type, strings.Join(supplied, "/"), pluralIs(len(supplied)), a.Transformer)
	}
	return nil
}

// pluralIs agrees the refusal's verb with the number of offending params.
func pluralIs(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// effectiveMaxBytes resolves the byte cap. Zero or negative selects the default
// rather than "no limit" — an unbounded extract is what the bounded-output rule
// forbids.
func effectiveMaxBytes(a collectArgs) int {
	if a.MaxBytes > 0 {
		return a.MaxBytes
	}
	return recipe.DefaultExtractMaxBytes
}

// renderExtract renders an extract result as text, applying the byte cap and
// stating any truncation explicitly.
//
// TRUNCATION IS ALWAYS DISCLOSED, in the header, for BOTH caps. A reader must
// be able to tell a complete extract from a bounded one without counting rows,
// which is the whole point of a bounded output that is not a silent cap. The
// header reports rows returned against rows MATCHED, so a run that cut 200 rows
// out of 1543 says so.
func renderExtract(a collectArgs, res *recipe.Result) string {
	ex := res.Extract
	if ex == nil {
		return fmt.Sprintf("extract: recipe=%s source=%s/%s rows=0/0 bytes=0\n",
			recipeLabel(a), a.Type, a.ID)
	}

	byteCap := effectiveMaxBytes(a)
	var body strings.Builder
	body.Grow(byteCap)
	kept, firstRowBytes := 0, 0
	for i, row := range ex.Rows {
		rendered := renderExtractRow(i, row)
		if i == 0 {
			firstRowBytes = len(rendered)
		}
		// BYTE ACCOUNTING CUTS AT ROW BOUNDARIES ONLY. A row is rendered whole
		// or not at all, so no field value is ever clipped mid-string and the
		// output stays parseable. The cap is checked BEFORE appending — a cap
		// enforced after the fact is not a cap.
		if body.Len()+len(rendered) > byteCap {
			ex.Truncated = true
			ex.TruncatedBy = "max_bytes"
			break
		}
		body.WriteString(rendered)
		kept++
	}
	ex.BytesReturned = body.Len()

	var out strings.Builder
	fmt.Fprintf(&out, "extract: recipe=%s source=%s/%s rows=%d/%d bytes=%d\n",
		recipeLabel(a), a.Type, a.ID, kept, ex.RowsMatched, ex.BytesReturned)
	out.WriteString(body.String())
	if ex.Truncated {
		out.WriteString(truncationDisclosure(a, ex, kept, firstRowBytes, byteCap))
	}
	return out.String()
}

// truncationDisclosure states which cap fired and what to do about it.
//
// THE ZERO-ROWS CASE IS THE ONE THAT MATTERS. When the byte cap admits no rows
// at all, an unqualified empty response is indistinguishable from a document
// that matched nothing, so the disclosure names the FIRST row's rendered size —
// the caller can then pick a max_bytes that works instead of staring at a blank
// answer. That is the loud answer to a state the caller must be told about, not
// a degraded lane.
func truncationDisclosure(a collectArgs, ex *recipe.ExtractResult, kept, firstRowBytes, byteCap int) string {
	capValue := effectiveMaxRowsForDisclosure(a)
	if ex.TruncatedBy == "max_bytes" {
		capValue = byteCap
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "TRUNCATED by %s=%d — returned %d of %d matched row(s), %d bytes.",
		ex.TruncatedBy, capValue, kept, ex.RowsMatched, ex.BytesReturned)
	if kept == 0 && ex.TruncatedBy == "max_bytes" {
		fmt.Fprintf(&sb, " NO rows fit: the first row alone renders to %d bytes.", firstRowBytes)
	}
	sb.WriteString(" Re-run with a larger cap, or narrow the recipe.\n")
	return sb.String()
}

// effectiveMaxRowsForDisclosure mirrors the interpreter's row-cap resolution so
// the disclosure names the cap that actually applied rather than the raw param.
func effectiveMaxRowsForDisclosure(a collectArgs) int {
	if a.MaxRows > 0 {
		return a.MaxRows
	}
	return recipe.DefaultExtractMaxRows
}

// recipeLabel names the recipe the run used — a saved name, or the inline
// literal when the body came from the caller.
func recipeLabel(a collectArgs) string {
	if a.RecipeBody != "" {
		return "inline"
	}
	return a.Recipe
}

// renderExtractRow renders one row: its index, emitted type, source node, and
// its fields in SORTED key order.
//
// The sort is required, not cosmetic: the emit block's fields are a Go map and
// Go randomizes map iteration, so unsorted output would differ run to run for
// identical input — and extract output exists to be compared across runs.
func renderExtractRow(i int, row recipe.ExtractRow) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- row %d type=%s src=%s\n", i, row.Type, row.SourceNodeID)
	names := make([]string, 0, len(row.Fields))
	for name := range row.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&sb, "%s: %s\n", name, row.Fields[name])
	}
	return sb.String()
}

// recipeRunOptions validates the recipe-selection params and builds the run
// options.
//
// It lives here rather than inline at the call site because the call site sits
// inside a line-range a landed gate pins, and every line added above that call
// eats margin the gate has none of.
//
// It does NOT re-check that an inline body requires extract mode — RunRecipe
// owns that rule, and duplicating it here would give it two owners that could
// drift.
func recipeRunOptions(a collectArgs) (recipe.Options, error) {
	if a.Recipe != "" && a.RecipeBody != "" {
		return recipe.Options{}, fmt.Errorf(
			"collect %s transformer=recipe: 'recipe' and 'recipe_body' are mutually exclusive — name a saved recipe or supply an inline body, not both", a.Type)
	}
	if a.Recipe == "" && a.RecipeBody == "" {
		return recipe.Options{}, fmt.Errorf(
			"collect %s transformer=recipe: 'recipe' (the recipe name) is required, or 'recipe_body' for an inline extract", a.Type)
	}
	// An inline body has no recipe node to name, and the manifest parser refuses
	// an empty key, so the inline path carries a fixed literal instead.
	recipeKey := a.Recipe
	if a.RecipeBody != "" {
		recipeKey = "inline"
	}
	return recipe.Options{
		SourceManifest: recipe.FormatSourceManifest(a.ID, recipeKey),
		Force:          a.Force,
		DryRun:         a.DryRun,
		Extract:        a.Extract,
		Body:           a.RecipeBody,
		// MaxBytes is deliberately absent — the byte cap is the renderer's.
		MaxRows: a.MaxRows,
	}, nil
}
