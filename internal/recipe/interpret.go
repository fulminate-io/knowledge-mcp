// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"time"
)

// Interpret evaluates a parsed Recipe against the in-memory sourceView,
// accumulating emissions into an in-memory Result. The caller (RunRecipe)
// supplies the target spec and source slug so StableID + lineage edges land
// deterministically, and ships the returned Result through the collector Sink
// afterwards — subject to the write guard, which refuses the whole write when an
// emitted id already names a differing row in the target.
//
// Unlike the former server interpreter, Interpret performs NO writes and opens
// NO transaction: every emit/link/lookup is recorded into the Result buffers and
// the in-run emitted set, never a target DB. Nothing downstream writes them
// either — the Result is read back by the caller and discarded.
func Interpret(
	ctx context.Context,
	recipe *Recipe,
	sv *sourceView,
	target TargetSpec,
	sourceSlug string,
	opts Options,
) (*Result, error) {
	if recipe == nil {
		return nil, fmt.Errorf("Interpret: nil recipe")
	}
	// EVERY VOCABULARY THE RECIPE NAMES IS CHECKED HERE, BEFORE THE CLOCK AND
	// BEFORE ANY ROW. The validator needs the source graph's type and name only
	// to render "<graphType>/<name>" in its refusals, and both travel on sv, so
	// this file's diff is one insertion and Interpret's signature is unchanged.
	compiledWhere, resolvedCompares, err := validateAgainstSource(recipe, sv)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	result := &Result{}
	// EXTRACT MODE ALLOCATES ITS RESULT UP FRONT, unconditionally. A lazily
	// allocated Extract is nil in exactly the state the disclosure exists to
	// reveal — every row skipped for an empty identity — so the response could
	// not tell "matched nothing" from "skipped everything". A nil Extract under
	// extract mode is a lie about what ran.
	if opts.Extract {
		result.Extract = &ExtractResult{}
	}
	// emitted is the in-run set of target node IDs this run has produced. It
	// gives evalLookup / evalLink their SAME-RUN scope: a lookup or link
	// resolves only against nodes emitted earlier in THIS interpretation, never
	// a cross-run read of the target graph (which the client cannot afford and
	// the server's in-txn target DB happened to provide).
	emitted := map[string]bool{}

	env := newEnv()
	// The validator compiled every where-tree regex for THIS run; the evaluator
	// reads them from here rather than from the shared, cached tree.
	env.whereRegexes = compiledWhere
	// The validator resolved every compare leaf's operator and operand for THIS
	// run, on the same terms: the evaluator reads them from here rather than
	// writing them onto the shared, cached tree.
	env.whereCompares = resolvedCompares
	for _, rule := range recipe.Rules {
		if err := dispatchRule(ctx, env, rule, sv, target, sourceSlug, opts, result, emitted); err != nil {
			result.Stats.ElapsedMillis = time.Since(start).Milliseconds()
			return result, err
		}
	}
	result.Stats.ElapsedMillis = time.Since(start).Milliseconds()
	return result, nil
}

// dispatchRule routes a Rule to the matching evaluator. The switch lives in its
// own function so Interpret stays under the funlen cap. Each case forwards the
// same ctx and opts unchanged.
func dispatchRule(
	ctx context.Context,
	env *Env,
	rule Rule,
	sv *sourceView,
	target TargetSpec,
	sourceSlug string,
	opts Options,
	result *Result,
	emitted map[string]bool,
) error {
	switch r := rule.(type) {
	case RuleSelect:
		return evalSelect(ctx, env, r, sv)
	case RuleTraverse:
		return evalTraverse(ctx, env, r, sv)
	case RuleWalk:
		return evalWalk(ctx, env, r, sv)
	case RuleFilter:
		return evalFilter(ctx, env, r, sv)
	case RuleBind:
		return evalBind(ctx, env, r, sv)
	case RuleGroupBy:
		return evalGroupBy(ctx, env, r, sv)
	case RuleSourceRef:
		return evalSourceRef(ctx, env, r, sv)
	case RuleEmit:
		return evalEmit(ctx, env, r, sv, target, sourceSlug, opts, result, emitted)
	case RuleLookup:
		return evalLookup(ctx, env, r, sv, target, sourceSlug, result, emitted)
	case RuleLink:
		return evalLink(ctx, env, r, sv, result, emitted)
	}
	return fmt.Errorf("unknown rule type %T at %d:%d", rule, rule.Position().Line, rule.Position().Col)
}
