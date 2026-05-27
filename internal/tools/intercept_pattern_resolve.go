// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/projects"
)

// patternResolution carries the validated + resolved pattern data shared by the
// create_plan and create_ticket interceptor paths. effectivePatternIDs /
// effectiveLangIDs are the proxy-resolved edge targets the BatchEdge wiring uses
// AS-IS; unresolvedIDs / unresolvedLangIDs land as node metadata; warnings are
// the non-fatal diagnostics surfaced under ## Warnings.
type patternResolution struct {
	effectivePatternIDs []string
	effectiveLangIDs    []string
	unresolvedIDs       []string
	unresolvedLangIDs   []string
	warnings            []string
}

// resolvePatternFields runs the wire-backed validate→resolve tail over the
// generic Execute seam (gc), shared by both create interceptors. It mirrors the
// legacy projects.CreatePlan / CreateTicket tail:
//
//	ValidatePatternFields (hard tristate + soft cross-graph lookup) →
//	ValidateLanguagePatterns → ResolvePatternIDsToEffectiveIDs(patternIDs) →
//	ResolvePatternIDsToEffectiveIDs(languagePatterns).
//
// A hard tristate violation surfaces as err so callers reject BEFORE any
// side-effect (notably the backend CreateTicket write-through). The effective
// (proxy) IDs MUST be written back onto the args before Build* runs, because
// wirePatternEdges / wireLanguagePatternEdges (and the ticket twins) use the ID
// slices AS-IS for their EdgeUses / EdgeAudits targets and BatchEdge targets
// cannot span graphs.
func resolvePatternFields(
	ctx context.Context,
	gc GraphCaller,
	patternIDs []string,
	noPatternsReason string,
	proposedPatterns []projects.ProposedPatternArgs,
	languagePatterns []string,
) (patternResolution, error) {
	warnings, unresolvedIDs, vErr := projects.ValidatePatternFields(ctx, gc, patternIDs, noPatternsReason, proposedPatterns)
	if vErr != nil {
		return patternResolution{}, vErr
	}
	langWarnings, unresolvedLangIDs := projects.ValidateLanguagePatterns(ctx, gc, languagePatterns)
	warnings = append(warnings, langWarnings...)

	effectivePatternIDs, rErr := projects.ResolvePatternIDsToEffectiveIDs(ctx, gc, patternIDs)
	if rErr != nil {
		return patternResolution{}, rErr
	}
	effectiveLangIDs, rErr := projects.ResolvePatternIDsToEffectiveIDs(ctx, gc, languagePatterns)
	if rErr != nil {
		return patternResolution{}, rErr
	}
	return patternResolution{
		effectivePatternIDs: effectivePatternIDs,
		effectiveLangIDs:    effectiveLangIDs,
		unresolvedIDs:       unresolvedIDs,
		unresolvedLangIDs:   unresolvedLangIDs,
		warnings:            warnings,
	}, nil
}
