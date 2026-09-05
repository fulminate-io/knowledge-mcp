// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	// pdfcollector supplies SourceSlug, the ONE definition of a pdf raw graph's
	// name. The import is doing two jobs: it also has the side effect of
	// registering "pdf" with collector.Register, which is why collect.go used to
	// carry it as a blank import. Moving it here as a named import keeps that
	// init in the same package, so registration is unaffected.
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/pdfcollector"
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
		"offset":      a.Offset != 0,
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
//
// THE HEADER ALSO DISCLOSES EVERY RUN COUNTER, and that is what makes an extract
// that MATCHED NOTHING distinguishable from one whose every row was SKIPPED for
// an empty identity. Both used to render rows=0/0 bytes=0 — byte-identical,
// measured end to end through the real interpreter — so the response could not
// tell a document with no matching sections from a recipe whose identity
// expression resolved empty on every row. The non-extract path already rendered
// the full Stats block; only this one was silent.
//
// THERE IS ONE HEADER RENDER, NOT TWO. The nil case is normalized at the top
// instead of branching to a second Fprintf, because a second render is HOW the
// old header kept printing while the disclosure was added to the other one.
// Folding them makes that class unreachable rather than merely covered.
func renderExtract(a collectArgs, res *recipe.Result) string {
	ex := res.Extract
	if ex == nil {
		ex = &recipe.ExtractResult{}
		res.Extract = ex
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
	st := res.Stats
	fmt.Fprintf(&out,
		"extract: recipe=%s source=%s/%s rows=%d/%d bytes=%d skipped=%d lookups_resolved=%d lookup_misses=%d link_misses=%d elapsed_ms=%d\n",
		recipeLabel(a), a.Type, a.ID, kept, ex.RowsMatched, ex.BytesReturned,
		st.SkippedChunks, st.LookupsResolved, st.LookupMisses, st.LinkMisses, st.ElapsedMillis)
	out.WriteString(body.String())
	if ex.Truncated {
		out.WriteString(truncationDisclosure(a, ex, kept, firstRowBytes, byteCap))
	}
	out.WriteString(offsetDisclosure(a, ex, kept))
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
	// THE NEXT OFFSET COUNTS RENDERED ROWS, NOT INTERPRETED ONES. This is a
	// two-stamper site: the interpreter stamps ex.RowsReturned in
	// recordExtractRow, the renderer stamps kept in renderExtract's row loop.
	// Under the byte cap the renderer keeps FEWER rows than the interpreter
	// captured, so resuming from RowsReturned would skip every row the byte cap
	// cut and the caller never saw. kept is the only one that describes what
	// actually reached them.
	if kept > 0 {
		fmt.Fprintf(&sb, " Next offset=%d.", a.Offset+kept)
	}
	sb.WriteString(" Re-run with a larger cap, or narrow the recipe.\n")
	return sb.String()
}

// offsetDisclosure names a page that starts past the end of the matched
// population, so an overshot cursor is distinguishable from a recipe that
// matched nothing.
//
// ALL FOUR CONDITIONS ARE LOAD-BEARING and each has a state that violates it.
// Without RowsMatched > 0 a genuinely empty result is reported as a cursor
// overshoot. Without kept == 0 a complete page grows a line it has no business
// carrying. Without the max_bytes clause a page the BYTE cap emptied is labeled
// an overshoot, printing three falsehoods directly beside the truncation line
// above it: the offset is not past the matched rows, the cursor did not empty
// the page, and "the last page starts at offset=0" sends the caller back to page
// one. And with Offset == 0 there is no cursor to have overshot.
//
// This is a DISCLOSURE, not a fallback: nothing is retried, defaulted or
// degraded. The caller is told which of several indistinguishable states
// occurred.
func offsetDisclosure(a collectArgs, ex *recipe.ExtractResult, kept int) string {
	if a.Offset <= 0 || kept != 0 || ex.RowsMatched <= 0 || ex.TruncatedBy == "max_bytes" {
		return ""
	}
	return fmt.Sprintf("OFFSET PAST END: offset=%d starts after the %d matched row(s); the last page starts at offset=%d.\n",
		a.Offset, ex.RowsMatched, lastPageOffset(a, ex.RowsMatched))
}

// lastPageOffset is the offset of the last page that holds real rows, under the
// row cap that actually applied — resolved through
// effectiveMaxRowsForDisclosure so the answer names the cap in effect rather
// than the raw param.
func lastPageOffset(a collectArgs, matched int) int {
	if matched <= 0 {
		return 0
	}
	rowCap := effectiveMaxRowsForDisclosure(a)
	return ((matched - 1) / rowCap) * rowCap
}

// effectiveMaxRowsForDisclosure mirrors the interpreter's row-cap resolution so
// the disclosure names the cap that actually applied rather than the raw param.
func effectiveMaxRowsForDisclosure(a collectArgs) int {
	if a.MaxRows > 0 {
		return a.MaxRows
	}
	return recipe.DefaultExtractMaxRows
}

// recipeLabel names the recipe the run used. Every admitted run is an inline
// body now, so it is always the inline literal; it stays a function because the
// header format string reads it as one.
func recipeLabel(_ collectArgs) string {
	return "inline"
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
//
// FORCE IS REFUSED HERE, FIRST, and the placement is the point. runRecipeCollect
// calls this function before recipe.RunRecipe, so the refusal is reached before
// any read, any interpret and any write — the same before-anything-happens
// position the target fences hold. Refusing is what keeps force from becoming a
// param the tool accepts and the recipe package ignores, which is worse than the
// param was: the caller reads a success and believes a destructive cleanup ran.
//
// The refusal deliberately does NOT live in rejectRecipeOnlyArgs. That function
// refuses recipe-only params on NON-recipe paths, the opposite direction. force
// remains valid for the code collector, whose own force plumbing in collect.go
// is untouched by this.
func recipeRunOptions(a collectArgs) (recipe.Options, error) {
	// PARAMS IS REFUSED HERE, PERMANENTLY, not "not yet supported". collect
	// declares params and routes it only to the external-collector path, so a
	// builtin recipe run accepted it and dropped it: the caller read a successful
	// response and believed the knob took effect. A declared-then-dropped param
	// is worse than one that does not exist, and no roadmap carries recipe
	// arguments — a not-yet phrasing would be a promise nothing backs.
	//
	// An ABSENT or EMPTY map is not an error. A refusal that fired
	// unconditionally would break every existing recipe call.
	if len(a.Params) > 0 {
		return recipe.Options{}, fmt.Errorf(
			"collect %s transformer=recipe: 'params' is not consumed by a builtin recipe run — a recipe takes no parameters. "+
				"Bind the value into the recipe body instead: `bind $x := \"...\"` gives a recipe-wide binding that survives the whole run", a.Type)
	}
	if a.Force {
		return recipe.Options{}, fmt.Errorf(
			"collect %s transformer=recipe: 'force' is retired for recipe runs — a recipe run returns rows and writes nothing, "+
				"so there is nothing for force to bypass", a.Type)
	}
	// DRY_RUN IS REFUSED HERE, PERMANENTLY, in the same shape and for the same
	// stated reason as force above it. dry_run meant "compute the projection but
	// skip the write"; there is no write to skip, so the knob names a distinction
	// that no longer exists. A declared-then-dropped param is worse than one that
	// does not exist.
	if a.DryRun {
		return recipe.Options{}, fmt.Errorf(
			"collect %s transformer=recipe: 'dry_run' is retired for recipe runs — a recipe run returns rows and writes nothing, "+
				"so there is no write for dry_run to skip; pass extract:true to read the rows back", a.Type)
	}
	// THE RECIPE NAME IS REFUSED HERE, PERMANENTLY, in the same shape and for the
	// same stated reason as the two refusals above it. Naming a saved recipe meant
	// loading a recipe node out of the transformers graph, and that family was
	// removed — so the param addresses nothing. Accepting it and running the
	// inline body anyway would be the declared-then-dropped shape this file
	// refuses everywhere else.
	//
	// The `recipe` field stays on collectArgs and in the schema so this refusal
	// can name the parameter the caller actually sent.
	if a.Recipe != "" {
		return recipe.Options{}, fmt.Errorf(
			"collect %s transformer=recipe: 'recipe' names a SAVED recipe, which is removed along with the transformers "+
				"graph family — recipes are ephemeral inline bodies now. Pass the body as 'recipe_body' with extract=true instead", a.Type)
	}
	if a.RecipeBody == "" {
		return recipe.Options{}, fmt.Errorf(
			"collect %s transformer=recipe: 'recipe_body' (an inline recipe body) is required", a.Type)
	}
	return recipe.Options{
		// An inline body has no recipe node to name, and the manifest parser
		// refuses an empty key, so every run carries this fixed literal.
		SourceManifest: recipe.FormatSourceManifest(a.ID, "inline"),
		Extract:        a.Extract,
		Body:           a.RecipeBody,
		// MaxBytes is deliberately absent — the byte cap is the renderer's.
		MaxRows: a.MaxRows,
		Offset:  a.Offset,
	}, nil
}

// resolveRawSourceGraphName turns a collect id into the name of the raw graph a
// recipe replay should read.
//
// IT IS IDENTITY FOR EVERY TYPE BUT PDF. A web id is ALREADY the graph name —
// the web collector sets its CollectResult's GraphName from the crawl options'
// Source, which is the collect id — so there is nothing to resolve. A pdf id is
// not: PDFCollector.Collect names the graph pdfcollector.SourceSlug(path), so a
// caller replaying a recipe against a document they hold the path for would
// otherwise read a graph named by the filesystem path, find nothing, and be told
// their recipe was wrong.
//
// filepath.IsAbs IS THE DISCRIMINATOR AND IT IS EXACT, not a heuristic:
// PDFCollector.Collect refuses a non-absolute id, so a stored pdf graph's slug
// can never itself be absolute and the two forms cannot collide.
func resolveRawSourceGraphName(collectType, id string) string {
	if collectType != "pdf" || !filepath.IsAbs(id) {
		return id
	}
	return pdfcollector.SourceSlug(id)
}

// rawSourceNotFoundHint returns the parenthetical to APPEND to a failed pdf
// replay's cause, naming both accepted id forms and the surface that lists what
// has been collected.
//
// IT IS APPENDED, NEVER SUBSTITUTED FOR THE CAUSE. A caller who hit a real
// interpreter refusal needs that refusal; a caller who named a document nobody
// collected needs the listing. Returning "" for every other case keeps the hint
// off messages it would not explain.
//
// The voice follows the client-side raw-graph refusals in
// intercept_query_webpdf.go, whose nameless-stats and discovery-search refusals
// already point at mode:"modules".
func rawSourceNotFoundHint(collectType, id, resolved string) string {
	if collectType != "pdf" || id == resolved {
		return ""
	}
	return fmt.Sprintf(
		" (read %s as an absolute path and resolved it to the collected graph name %q; "+
			`both forms are accepted — use query(graph:"pdf", mode:"modules") to list the pdf graphs already collected)`,
		id, resolved)
}
