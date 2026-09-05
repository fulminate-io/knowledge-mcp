// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// RunRecipe is the single client-side entry point for a recipe transform — the
// collapsed replacement for the former server-side transform orchestration +
// Interpret + registry. There is NO transform interface, NO registry, and NO
// init(): the `collect` tool calls this plain exported function directly.
//
// THE READ COST IS PER PAGE, NEVER PER ROW. loadSourceView drains the source
// graph in bounded pages, so its RPC count scales with the graph rather than
// being fixed — ceil(nodes/BrowsePageSize) + ceil(ids/EdgePivotPageSize)
// sequential round trips. What holds, and is the property this design depends
// on, is that NO per-row Execute is issued during the load or during any
// subsequent interpretation read.
//
// A RECIPE RUN WRITES NOTHING. It returns rows on Result.Extract and ships them
// to no sink and no graph. The write path — a target pre-read, a refuse-on-
// collision guard and a Sink ship — was removed with the saved recipes that
// carried the target address; there is no target to write to and no verb that
// writes.
//
// expectedSourceType, when non-empty, is validated against the recipe's
// source_graph_type metadata: a mismatch (e.g. a `collect type=pdf` against a
// web-source recipe) returns a typed error naming BOTH types before any source
// read. Pass "" to skip the check (callers that already know the type matches).
//
// RUNNING A SAVED RECIPE BY NAME IS REMOVED, along with the transformers graph
// family that stored the recipe nodes. Every run is an INLINE body now, so a
// bodyless call is a loud refusal naming the surviving path rather than a load
// against a graph that no longer exists.
//
// Sequence:
//  1. ParseSourceManifest(opts.SourceManifest) → sourceSlug, recipeName.
//  2. runInlineExtract: parse opts.Body, take the source type from the caller,
//     load the source view, Interpret, and return the captured rows.
//
// Stats are returned in the Result for the caller (the collect tool) to
// surface. Nothing is written back anywhere.
func RunRecipe(
	ctx context.Context,
	caller foundation.GraphCaller,
	sourceGraphName string,
	expectedSourceType kgtypes.GraphType,
	opts Options,
) (*Result, error) {
	start := time.Now()
	sourceSlug, recipeName, err := ParseSourceManifest(opts.SourceManifest)
	if err != nil {
		return nil, fmt.Errorf("recipe: parse manifest: %w", err)
	}
	// INLINE BODY is the only path. It parses what the caller supplied and never
	// reads a recipe bucket.
	if opts.Body != "" {
		return runInlineExtract(ctx, caller, sourceGraphName, expectedSourceType, sourceSlug, opts, start)
	}

	// A BODYLESS CALL IS A LOUD ERROR, not a nil dereference. The saved arm used
	// to load the named recipe out of the transformers graph; that family is gone,
	// so there is nothing left to load and the refusal says so, names the family
	// that was removed, and names the parameter that still works.
	return nil, fmt.Errorf(
		"recipe %q: running a SAVED recipe by name is removed along with the transformers graph family — "+
			"recipes are ephemeral inline bodies now. Pass the body as collect's `recipe_body` with extract=true; "+
			"see help(\"recipes\")", recipeName)
}

// astCache memoizes parsed recipes across RunRecipe invocations. Every entry is
// now keyed by parseInlineWithCache on a hash of the body's CONTENT, so an edited
// body is a different key and there is nothing to invalidate.
var astCache sync.Map // astCacheKey → *Recipe

// astCacheKey carries only the id. It used to carry the recipe node's UpdatedAt
// as well, because the saved path keyed on (node id, update time); the inline
// path keys on a content hash, which already changes when the body does.
type astCacheKey struct {
	id string
}

// runInlineExtract is the INLINE-BODY path: parse the caller's body, read the
// source graph, interpret, and return the captured rows. It never writes.
//
// TWO VALUES THE SAVED PATH GETS FROM THE RECIPE NODE HAVE NO SOURCE HERE, so
// both are supplied explicitly rather than left to a default:
//
// The RECIPE KEY comes from the caller's manifest, which the tool layer builds
// with a fixed literal. The manifest parser refuses an empty key, so an inline
// run with a blank one fails before it parses anything.
//
// The SOURCE GRAPH TYPE comes from expectedSourceType — the collect type the
// caller already passes. Without it there is no graph to read, and guessing one
// would read the wrong document, so an empty value is a typed error naming the
// parameter rather than a fallback.
//
// The saved path's target metadata block is skipped entirely: it validates a
// write target that an inline extract does not have and does not need.
func runInlineExtract(
	ctx context.Context,
	caller foundation.GraphCaller,
	sourceGraphName string,
	expectedSourceType kgtypes.GraphType,
	sourceSlug string,
	opts Options,
	start time.Time,
) (*Result, error) {
	// EXTRACT IS THE ONLY MODE, and the refusal states the MECHANICAL reason
	// only. A recipe run returns rows and writes nothing, so there is no other
	// mode for it to be in. It used to prescribe saving the body to freeze the
	// extraction; recipes are ephemeral and nothing is frozen, and a refusal that
	// prescribes a retired workflow is worse than one that prescribes nothing.
	if !opts.Extract {
		return nil, fmt.Errorf(
			"recipe: a recipe run returns rows and writes nothing — pass extract:true to read the rows back")
	}
	if expectedSourceType == "" {
		return nil, fmt.Errorf(
			"recipe: an inline recipe body needs the source graph type — pass the collect `type` param (for example web or pdf)")
	}

	ast, err := parseInlineWithCache(opts.Body)
	if err != nil {
		return nil, err
	}
	sv, err := loadSourceView(ctx, caller, expectedSourceType, sourceGraphName)
	if err != nil {
		return nil, err
	}
	// The sentinel target is confined to in-run stable-id bookkeeping: extract
	// skips the write, so it never reaches storage. Its NAME is the source slug
	// so two inline runs over different documents cannot collide in that
	// bookkeeping.
	target := TargetSpec{GraphType: extractSentinelGraphType, Name: sourceSlug}
	result, err := Interpret(ctx, ast, sv, target, sourceSlug, opts)
	if err != nil {
		return result, err
	}
	result.Stats.ElapsedMillis = time.Since(start).Milliseconds()
	return result, nil
}

// parseInlineWithCache parses an inline body, memoizing on the body's CONTENT.
//
// THE CONTENT HASH IS NOT A STYLE CHOICE. The shared cache is keyed on a recipe
// node's id and update time, neither of which an inline body has. A synthetic
// constant key would make every inline body in a process collide onto the FIRST
// one's parsed form — observed directly: three different bodies sharing one key
// all executed the first body's rules — and it would pass any test that ran a
// single inline body per process.
func parseInlineWithCache(body string) (*Recipe, error) {
	sum := sha256.Sum256([]byte(body))
	key := astCacheKey{id: "inline:" + hex.EncodeToString(sum[:])}
	if v, ok := astCache.Load(key); ok {
		ast, ok := v.(*Recipe)
		if !ok {
			return nil, fmt.Errorf("parseInlineWithCache: cache entry has unexpected type %T", v)
		}
		return ast, nil
	}
	ast, err := Parse([]byte(body))
	if err != nil {
		return nil, fmt.Errorf("inline recipe parse: %w", err)
	}
	astCache.Store(key, ast)
	return ast, nil
}
