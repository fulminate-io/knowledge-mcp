// SPDX-License-Identifier: Apache-2.0

package tools

// collect_args.go holds the collect tool's wire-shape struct. It is a sibling of
// collect.go rather than part of it because that file sits against the repo's
// 500-line per-file ceiling and the recipe landing's `land` field pushed it over;
// the split is by bytes, not by concern, and the struct is meaningful only to the
// handlers in collect.go. The same reason wire_persist_types.go sits beside
// wire_persist.go.

// collectArgs is the client-side collect command argument contract. It lives
// only in the client binary (collection runs client-side after the binary
// split). Type-specific fields are zero-valued when type does not match and
// ignored by other dispatch paths.
type collectArgs struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Force   bool   `json:"force"`
	Promote bool   `json:"promote,omitempty"` // code only: force base + repoint default branch to the collected branch

	// Params is the generic param passthrough for a registered (non-builtin)
	// custom collector: the whole object rides the MCP tool call as its `params`
	// argument, beside the collect id, and is validated against the schema the
	// PROVIDER advertised for that tool before the call is made. The built-in
	// collectors (code/web/pdf) ignore it and read their typed fields
	// below instead.
	Params map[string]any `json:"params,omitempty"`

	// Web-specific. Threaded through ctx via web.WithCrawlOptions.
	SeedURLs          []string `json:"seed_urls,omitempty"`
	FollowPatterns    []string `json:"follow_patterns,omitempty"`
	MaxDepth          int      `json:"max_depth,omitempty"`
	MaxPages          int      `json:"max_pages,omitempty"`
	MaxPathSegments   int      `json:"max_path_segments,omitempty"`
	MaxPagesPerHost   int      `json:"max_pages_per_host,omitempty"`
	MaxConcurrency    int      `json:"max_concurrency,omitempty"`
	MaterializeGithub bool     `json:"materialize_github,omitempty"`
	PolitenessMs      int      `json:"politeness_ms,omitempty"`
	UserAgent         string   `json:"user_agent,omitempty"`
	MaxDownloadBytes  int64    `json:"max_download_bytes,omitempty"`

	// Web/PDF transformer dispatch. Transformer="recipe" runs the recipe
	// transform CLIENT-SIDE via recipe.RunRecipe (see runRecipeCollect). Recipe
	// and DryRun are both REFUSED BY NAME and are parsed only so the refusal can
	// say what the caller sent; any other Transformer value is rejected.
	//
	// Extract and Land are the two MODES, and a run needs at least one. Extract
	// returns the emitted rows for inspection, bounded by MaxRows and MaxBytes and
	// windowed by Offset. Land WRITES them into the combined practice graph under
	// a source hub, and refuses MaxRows and Offset outright: a landing writes the
	// whole emitted set, so a row window would bound the render and not the write.
	//
	// THE LANDING FLAG IS NOT NAMED `source`. That name was taken on this struct
	// by the logs collector, whose fields left with the built-in log collectors;
	// the flag KEEPS its name because `land` is the advertised wire spelling and
	// renaming an advertised param is a wire change, not a merge cleanup.
	//
	// Body is an INLINE recipe body, and is the only form there is. MaxBytes
	// deliberately does NOT travel through recipe.Options — only the renderer
	// knows rendered sizes, so the byte cap is applied there. Offset DOES travel
	// through recipe.Options: the cursor is applied where the rows are captured.
	Transformer string `json:"transformer,omitempty"`
	Recipe      string `json:"recipe,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Extract     bool   `json:"extract,omitempty"`
	Land        bool   `json:"land,omitempty"`
	RecipeBody  string `json:"recipe_body,omitempty"`
	MaxRows     int    `json:"max_rows,omitempty"`
	MaxBytes    int    `json:"max_bytes,omitempty"`
	Offset      int    `json:"offset,omitempty"`
}
