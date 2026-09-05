// SPDX-License-Identifier: Apache-2.0

// corpus_check_gate.go — the pre-write admission gate for check-carrying
// checks-graph nodes. "No fixture, no admission" is a property of the write
// path here, not a documented intention: every mutate route that can write
// check metadata runs this guard before anything reaches the store.

package tools

import (
	"context"
	"fmt"
	"maps"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
)

// checksGraphSelector is the wire value of the graph param that addresses the
// corpus-check graph — the string form of kgtypes.GraphChecks. Named once here
// so the gate's routing and its fixture resolution cannot drift apart.
const checksGraphSelector = string(kgtypes.GraphChecks)

// checksGraphInstance is the instance name sent on the wire for the ONE checks
// graph, and it is deliberately EMPTY.
//
// checks is a singleton, so the server's selector policy declares it consumes no
// instance field and REJECTS a set name. render.graphTarget passes
// omitDefaultName=false — it takes an explicit name at its word, including the
// literal "default" — so anything non-empty here would be sent and refused.
const checksGraphInstance = ""

// guardCheckIDQualified requires a MINTED check id to be namespaced by the
// check's own language, as "<language>:<rest>".
//
// WHY THIS IS SCOPED TO MINTS, which is the whole subtlety. The collision this
// prevents happens when an id COMES INTO EXISTENCE: two languages' authors pick
// the same name and the second write lands on the first's node. A create does
// not carry an id — the store generates one, unique by construction — so there
// is nothing to qualify. Requiring a prefix unconditionally would reject every
// ordinary create, and requiring one of FIXTURES would reject generated fixture
// ids too, which is why the cross-language guard on fixtures is a metadata check
// rather than an id-shape rule.
//
// EDITING AN EXISTING NODE IS NOT A MINT, and conflating the two closed the
// authoring loop entirely. An update must name the node it edits, and for a
// check created through the ordinary create path that name IS the store's
// generated id — which no author can retroactively give a language prefix. So
// qualifying every caller-supplied id made every generated-id check permanently
// unrevisable: create assigned an id that update then refused. `exists` is what
// separates the two acts. It cannot weaken the guard, because an id that already
// resolves has, by definition, already survived it or was generated.
//
// An empty language cannot happen here: ParseCheck has already validated it
// against the tree-sitter vocabulary before this runs.
func guardCheckIDQualified(id, language string, exists bool) error {
	if id == "" {
		return nil // generated id; unique by construction.
	}
	if exists {
		return nil // an edit of a node that already exists mints nothing.
	}
	prefix := language + ":"
	if strings.HasPrefix(id, prefix) {
		return nil
	}
	return fmt.Errorf(
		"check id %q must be namespaced by its language as %q so two languages' checks cannot collide "+
			"on one id in the single checks graph", id, prefix+"<name>")
}

// contractKeys is the check vocabulary, used ONLY for the cheap skip below.
// A payload mentioning none of these cannot change any check's validity, so it
// pays no reads at all.
//
// THE TEST-FILE DECLARATION IS ON THE LIST, and it is the one entry whose place
// here is a judgement rather than an obvious fact. It changes no pattern and no
// fixture binding; what it changes is WHICH FILES the check walks, which is the
// scope its fixtures were admitted under. The conservative arm is taken
// deliberately: admission already runs fixtures with tests included, so
// re-running it costs two fixture reads and one in-memory validation on an
// authoring path and nothing at all on any scan — while the alternative is a
// skip list that decides, key by key, which writes may bypass a gate. A list
// like that is one omission away from admitting a check nobody validated, which
// is the shape the bad-input-always-errors rule exists to refuse.
var contractKeys = []string{
	corpus.MetaCheckType,
	corpus.MetaSeverity,
	corpus.MetaLanguage,
	corpus.MetaDSLPattern,
	corpus.MetaCheckWhere,
	corpus.MetaFixtureBad,
	corpus.MetaFixtureGood,
	corpus.MetaLLMOnly,
	corpus.MetaAppliesToTests,
}

// updateShapedOps are the operations whose metadata MERGES per key with what the
// node already carries. For those, the payload alone is not the node the write
// produces, so validating the payload alone would let an update that changes
// only dsl_pattern past a gate that never saw its check_type.
var updateShapedOps = map[string]bool{
	"update":               true,
	"upsert":               true,
	"update_batch":         true,
	"bulk_update_metadata": true,
}

// guardCorpusCheckWrite refuses a checks-graph write that would admit a check
// whose fixtures have not been run.
//
// It reads all four metadata carriers a mutate payload can use, merges each
// against the node it targets for update-shaped ops, parses the result against
// the corpus contract, resolves the two fixture examples in the check's OWN
// checks graph, and dispatches to the validator that owns the check type.
//
// GATED ON graph="checks" ALONE, and the narrowness is the point. Checks and
// their fixtures live in their own per-language graph; practice graphs hold
// prose guidance and model entries, which carry no check_type and are not this
// gate's business. A gate that accepted BOTH graphs would make the split
// unobservable — a check written to the old location would still be admitted,
// and nothing downstream could tell a working retarget from a reader that takes
// either.
//
// Returns nil for every write that is not a check write — the vast majority.
func guardCorpusCheckWrite(ctx context.Context, gc GraphCaller, a mutateArgs) error {
	if a.Graph != checksGraphSelector {
		return nil
	}
	candidates := payloadMetadataMaps(a.raw)
	if !mentionsContractKey(candidates) {
		return nil
	}
	merge := updateShapedOps[a.Operation]
	for _, cand := range candidates {
		md := cand.Metadata
		candID := cand.ID
		if candID == "" {
			candID = a.ID
		}
		// exists answers "does candID already name a node", which is what
		// separates an EDIT from a MINT for the id-qualification guard below. A
		// non-merge op never reads, and its id is either absent (create) or a
		// name the author chose — both correctly read as not-existing.
		exists := false
		if merge {
			var err error
			md, exists, err = mergeOverCurrent(ctx, gc, candID, md)
			if err != nil {
				return err
			}
		}
		if err := guardCheckCandidate(ctx, gc, candID, cand.Path, md, exists); err != nil {
			return err
		}
	}
	return nil
}

// mentionsContractKey reports whether any candidate names a contract key.
func mentionsContractKey(candidates []payloadMetadata) bool {
	for _, cand := range candidates {
		for _, k := range contractKeys {
			if _, ok := cand.Metadata[k]; ok {
				return true
			}
		}
	}
	return false
}

// mergeOverCurrent layers the payload's metadata over the target node's current
// metadata, which is what a per-key metadata merge actually produces. The second
// return reports whether the target RESOLVED, which the caller needs to tell an
// edit from a mint.
//
// A node that does not resolve is an upsert acting as a create: there is nothing
// to merge over, so the payload alone IS the resulting node. That branch only
// became reachable once FetchNodeIn stopped leaking the engine's NOT_FOUND as an
// error — before that, an upsert naming a new id failed here on the read, and
// the create-shaped upsert this comment describes could not happen at all.
func mergeOverCurrent(
	ctx context.Context, gc GraphCaller, id string, payload map[string]string,
) (map[string]string, bool, error) {
	if id == "" {
		return payload, false, nil
	}
	node, err := render.FetchNodeIn(ctx, gc, id, checksGraphSelector, checksGraphInstance)
	if err != nil {
		return nil, false, fmt.Errorf("mutate: corpus check gate: read %q: %w", id, err)
	}
	if node == nil {
		return payload, false, nil
	}
	merged := make(map[string]string, len(node.GetMetadata())+len(payload))
	maps.Copy(merged, node.GetMetadata())
	maps.Copy(merged, payload)
	return merged, true, nil
}

// guardCheckCandidate applies the contract to one resulting metadata map.
//
// The three-way branch on ParseCheck's return is deliberate and follows the
// contract's return table: isCheck true is an executable check to validate;
// isCheck false with LLMOnly true is the honest prose lane, which carries no
// fixtures BY CONTRACT and must be admitted unchanged; isCheck false with
// LLMOnly false is not a check at all and this gate does not look at it.
func guardCheckCandidate(
	ctx context.Context, gc GraphCaller, id, path string, md map[string]string, idExists bool,
) error {
	c, isCheck, err := corpus.ParseCheck(&knowledgev1.Node{Metadata: md})
	if err != nil {
		return fmt.Errorf("mutate: %s: %w", path, err)
	}
	if !isCheck {
		return nil
	}
	if err := guardCheckIDQualified(id, string(c.Language), idExists); err != nil {
		return fmt.Errorf("mutate: %s: %w", path, err)
	}
	bad, err := fetchFixture(ctx, gc, string(c.Language), corpus.MetaFixtureBad, c.FixtureBad)
	if err != nil {
		return fmt.Errorf("mutate: %s: %w", path, err)
	}
	good, err := fetchFixture(ctx, gc, string(c.Language), corpus.MetaFixtureGood, c.FixtureGood)
	if err != nil {
		return fmt.Errorf("mutate: %s: %w", path, err)
	}
	if err := validateCheckFixtures(ctx, c, bad, good); err != nil {
		return fmt.Errorf("mutate: %s: %w", path, err)
	}
	return nil
}

// fetchFixture resolves one fixture binding in the check's own checks graph and
// enforces the three things a fixture must be: resolvable, an example node,
// and non-empty. Each failure names the binding and the id, because "validation
// failed" without them sends an author looking through every node they own.
//
// The fixture is resolved in the SAME graph as the check, never in practice/
// <language>: checks and their fixtures move together, so the join stays inside
// one graph and a fixture id that only resolves in the old location is a genuine
// dangling reference rather than a cross-graph lookup to fall back on.
func fetchFixture(ctx context.Context, gc GraphCaller, language, key, id string) (corpus.Fixture, error) {
	node, err := render.FetchNodeIn(ctx, gc, id, checksGraphSelector, checksGraphInstance)
	if err != nil {
		return corpus.Fixture{}, fmt.Errorf("%s=%q: %w", key, id, err)
	}
	if node == nil {
		return corpus.Fixture{}, fmt.Errorf("%s=%q does not resolve in the %s graph", key, id, checksGraphSelector)
	}
	if kgtypes.NodeType(node.GetType()) != kgtypes.NodeExample {
		return corpus.Fixture{}, fmt.Errorf("%s=%q is a %s node, not an %s", key, id, node.GetType(), kgtypes.NodeExample)
	}
	// THE CROSS-LANGUAGE BINDING CHECK, and it only became necessary when the
	// per-language graphs collapsed into one. Previously a Go check could not
	// name a Python fixture because they were in different graphs and the id
	// simply would not resolve. Now every fixture is one id lookup away, so a
	// mistyped or copy-pasted binding silently validates a Go check against
	// Python source — which usually makes the check look SILENT on its bad
	// example and get refused for the wrong reason, or worse, pass by accident.
	if fxLang := node.GetMetadata()[corpus.MetaLanguage]; fxLang != language {
		return corpus.Fixture{}, fmt.Errorf(
			"%s=%q carries %s=%q but the check declares %s=%q — a fixture must be written in the check's own language",
			key, id, corpus.MetaLanguage, fxLang, corpus.MetaLanguage, language)
	}
	if strings.TrimSpace(node.GetContent()) == "" {
		return corpus.Fixture{}, fmt.Errorf("%s=%q carries no content, so there is nothing to run the check against", key, id)
	}
	return corpus.Fixture{ID: id, Content: node.GetContent()}, nil
}

// validateCheckFixtures dispatches by check type to the validator that owns that
// type's fixture semantics.
//
// Dispatch is a CALLER concern on purpose: each validator stays in the package
// that owns its semantics, no validator moves, and the contract package takes on
// no dependency it does not need. The validator's error is returned UNCHANGED so
// errors.Is classification is uniform across arms and no consumer parses text.
func validateCheckFixtures(ctx context.Context, c corpus.Check, bad, good corpus.Fixture) error {
	switch c.Type {
	case corpus.CheckAstPattern:
		return corpus.ValidateFixtures(ctx, c, bad, good)
	case corpus.CheckGraphAssertion, corpus.CheckTopologyThreshold:
		// Both are assertions over a NODE/EDGE SET rather than over a pattern
		// walk, so both are owned by the validator that turns a fixture snippet
		// into graph facts and runs the same evaluator the scan uses.
		return corpusscan.ValidateGraphFixtures(ctx, c, bad, good)
	default:
		// corpus.ValidateFixtures names the type and returns ErrNoExecutor, so
		// an unhandled type is REFUSED rather than admitted unvalidated.
		return corpus.ValidateFixtures(ctx, c, bad, good)
	}
}
