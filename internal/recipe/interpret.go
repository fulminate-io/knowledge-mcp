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
// deterministically, runs any Force cleanup BEFORE calling Interpret, and ships
// the returned Result through the collector Sink afterwards.
//
// Unlike the former server interpreter, Interpret performs NO writes and opens
// NO transaction: every emit/link/lookup is recorded into the Result buffers and
// the in-run emitted set, never a target DB. DryRun therefore differs only in
// what RunRecipe does with the Result (skip the Sink write) — the interpretation
// itself is identical either way.
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

	start := time.Now()
	result := &Result{}
	// emitted is the in-run set of target node IDs this run has produced. It
	// gives evalLookup / evalLink their SAME-RUN scope: a lookup or link
	// resolves only against nodes emitted earlier in THIS interpretation, never
	// a cross-run read of the target graph (which the client cannot afford and
	// the server's in-txn target DB happened to provide). DryRun and live runs
	// populate it identically.
	emitted := map[string]bool{}

	env := newEnv()
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
