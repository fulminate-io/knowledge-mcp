// SPDX-License-Identifier: Apache-2.0

package projects

// DeriveCriterionSummary produces the auto-derived summary for a criterion
// node from its (already-defaulted) type, description, and command. It is the
// single source for the criterion-summary format: callers that previously
// concatenated these strings inline (BuildCriterionNode, upsertCriterionNode)
// route through this function so the persisted summary cannot drift between
// the node builder and the client validator. The output is byte-identical to
// the historical inline expressions — do NOT change the format (the persisted
// summaries of existing nodes depend on it).
//
// cType is the already-defaulted criterion type (callers pass "manual" when
// the supplied type is empty); this function does not default it.
func DeriveCriterionSummary(cType, description, command string) string {
	summary := cType + " criterion: " + description
	if command != "" {
		summary = cType + " criterion: " + description + " (" + command + ")"
	}
	return summary
}

// DeriveFindingSummary produces the auto-derived summary for a finding node
// from its description and optional evidence, used when a caller supplies no
// explicit summary. It is the single source for the finding-summary fallback
// (buildFindingNode on create, the per-type update re-derive). The output is
// byte-identical to the historical inline expression at buildFindingNode — do
// NOT change the format (existing nodes' persisted summaries depend on it).
func DeriveFindingSummary(description, evidence string) string {
	summary := description
	if evidence != "" {
		summary += ". Evidence: " + evidence
	}
	return summary
}

// DeriveRuleSummary produces the auto-derived summary for a rule node from its
// name and optional scope, used when a caller supplies no explicit summary. It
// is the single source for the rule-summary fallback (handleClientMutateCreate-
// Rule on create, the per-type update re-derive). The output is byte-identical
// to the historical inline expression — do NOT change the format.
func DeriveRuleSummary(name, scope string) string {
	summary := "Rule: " + name
	if scope != "" {
		summary += " (scope: " + scope + ")"
	}
	return summary
}

// DeriveQuestionSummary produces the auto-derived summary for an open-question
// node from its question text and optional context. It is the single source
// for the question-summary fallback used when a caller supplies no explicit
// summary (BuildPlanGraph open_questions, buildResearchGraph). The output is
// byte-identical to the historical inline expressions — do NOT change the
// format.
func DeriveQuestionSummary(question, context string) string {
	summary := "Question: " + question
	if context != "" {
		summary += ". Context: " + context
	}
	return summary
}

// DerivePatternSummary produces the auto-derived summary for an eager-created
// "emerging" pattern node from its name and optional sketch. proposed_patterns
// supply only name + sketch (no summary), but pattern nodes are summary-required
// (embed-only) — an empty summary fails create-time validation and rolls back
// the whole create_batch. Derive a non-empty one here. Mirrors the other
// Derive*Summary helpers; do NOT change the format.
func DerivePatternSummary(name, sketch string) string {
	summary := "Proposed pattern: " + name
	if sketch != "" {
		summary += ". Sketch: " + sketch
	}
	return summary
}
