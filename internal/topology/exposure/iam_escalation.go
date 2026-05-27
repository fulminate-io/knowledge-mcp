// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation.go implements the IAMEscalationAnalyzer — a cloud-graph
// analyzer that surfaces IAM privilege escalation paths from non-admin
// principals to effective-admin principals via the full PMapper-style
// rule set in iam_rules*.go.
//
// RULES (v2): 26 rules — 7 v1.1 baseline + 19 v2 additions across six
// PMapper categories:
//
//   - credential access (2):   create_login_profile, update_login_profile
//   - policy modification (5): put_user_policy, put_group_policy,
//                              put_role_policy, create_policy_version,
//                              set_default_policy_version
//   - identity management (1): add_user_to_group
//   - trust modification (1):  update_assume_role_policy
//   - compute PassRole (8):    glue_create_dev_endpoint,
//                              sagemaker_create_notebook,
//                              sagemaker_create_training,
//                              codebuild_create_project,
//                              codebuild_update_project,
//                              cloudformation_create_stack,
//                              datapipeline_create_pipeline, ecs_run_task
//   - Lambda update (2):       update_function_code,
//                              update_function_configuration
//
// Each rule declares a per-rule confidence constant at registerIAMRule
// time (OQ-4): 1.0 for direct admin-equivalent ops, 0.9 for high-signal
// indirect promotions, 0.7 for pessimistic cases like
// set_default_policy_version. dispatchIAMRules stamps the constant onto
// every emitted edge; path min_confidence (weakest link) is a Finding
// metric. Cross-account BFS (OQ-7) walks (account, id) tuples so A→B→A
// trust cycles are preserved and transitions set has_cross_account=1.
//
// ARCHITECTURE: Run binds a per-account cloud reader to req.Name;
// dispatchAcrossAccounts invokes every rule across every loaded account;
// findEscalationPaths BFS from each non-admin source over inferred + native
// EdgeAssumesRole edges until admin or maxDepth; dedupFindings merges
// (source, terminal) collisions; findings sort shortest-first and cap at
// req.TopK.
//
// READ-ONLY: the analyzer never mutates any graph.

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// maxEscalationDepth caps BFS hops. Six hops is plenty for any realistic
// escalation chain — most published PMapper findings are 1-3 hops.
const maxEscalationDepth = 6

// IAMEscalationAnalyzer is the topology.Analyzer that surfaces IAM
// privilege escalation paths. Zero-value usable.
type IAMEscalationAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (IAMEscalationAnalyzer) Name() string { return "iam_escalation" }

// Run executes the analyzer for one cloud account (req.Name). Returns nil,
// nil for non-cloud graphs to keep the analyzer harmless when dispatched
// against the wrong graph type.
func (a IAMEscalationAnalyzer) Run(ctx context.Context, req Request) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/iam_escalation: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, nil
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/iam_escalation: req.Caller must not be nil")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("topology/iam_escalation: req.Name (account) must not be empty")
	}

	scoped := newCloudReader(req.Caller, req.Name)

	rctx, err := buildIAMRuleContext(ctx, req.Caller, scoped, req.Name)
	if err != nil {
		return nil, err
	}
	if len(rctx.Roles)+len(rctx.Users)+len(rctx.Groups) == 0 {
		return nil, nil
	}

	// Phase 9.5: dispatch rules across every loaded cloud account so the
	// BFS has a global view of the inferred edges and admin set. Each
	// account's rule dispatch produces edges stamped with its own account
	// ID (iamInferredEdge.Account); merged together they form the
	// cross-account escalation graph.
	inferred, admins, scopedByAccount, err := dispatchAcrossAccounts(ctx, req.Caller, rctx)
	if err != nil {
		return nil, err
	}

	sources := buildSourceSet(rctx, inferred, admins)
	lookup := func(account string) *cloudReader {
		if rd, ok := scopedByAccount[account]; ok {
			return rd
		}
		return nil
	}
	paths := findEscalationPaths(ctx, lookup, inferred, admins, sources, req.Name, maxEscalationDepth)

	// Phase 9 Step 3: dedup by (source, terminal) tuple so multiple rules
	// reaching the same admin state merge into one finding whose Evidence
	// enumerates every contributing rule and whose min_confidence metric
	// tracks the weakest link.
	findings := dedupFindings(ctx, req, paths, inferred)
	sortEscalationFindings(findings)
	if req.TopK > 0 && len(findings) > req.TopK {
		findings = findings[:req.TopK]
	}
	return findings, nil
}

// buildIAMRuleContext loads every iam-role, iam-user, and iam-group node in
// the scoped account and packages them with the wire caller into the
// iamRuleContext that rules consume.
func buildIAMRuleContext(ctx context.Context, caller foundation.GraphCaller, scoped *cloudReader, account string) (*iamRuleContext, error) {
	rctx := &iamRuleContext{
		caller:      caller,
		scoped:      scoped,
		Account:     account,
		policyCache: make(map[string][]*IAMPolicy),
	}
	nodes, err := scoped.cloudResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("topology/iam_escalation: list nodes: %w", err)
	}
	for _, n := range nodes {
		switch nodeMeta(n, "resource_type") {
		case "iam-role":
			rctx.Roles = append(rctx.Roles, n)
		case "iam-user":
			rctx.Users = append(rctx.Users, n)
		case "iam-group":
			rctx.Groups = append(rctx.Groups, n)
		case "iam-policy":
			rctx.Policies = append(rctx.Policies, n)
		case "lambda-function":
			rctx.Functions = append(rctx.Functions, n)
		}
	}
	return rctx, nil
}

// dispatchIAMRules invokes every registered IAM rule and aggregates the
// inferred edges into a map keyed by source principal. The admin set
// contains every principal whose effective_admin or attach_policy
// self-loops indicate (per OQ-6) automatic admin promotion.
//
// Per Phase 9 Step 1 (OQ-4), every emitted edge is stamped with the
// registering rule's per-rule confidence constant (unless the rule already
// set a non-zero Confidence explicitly) and the rule's registration name.
// Downstream dedup (Step 3) uses these to enumerate contributing rules and
// take the minimum confidence across merged findings.
//
// Phase 9 Step 2 (boundary wire-up): after a rule emits its edges, each
// edge is gated by edgeAllowedByBoundary — if the source principal has
// a parseable permissions boundary that does NOT allow one of the
// actions the rule declared at registration time, the edge is dropped.
// This honors AWS's "intersection of identity + boundary" semantics and
// eliminates false positives on boundary-restricted principals. Rules
// with nil Actions (e.g. assume_role_trust_policy) bypass the filter.
func dispatchIAMRules(ctx context.Context, rctx *iamRuleContext) (
	inferred map[string][]iamInferredEdge,
	admins map[string]bool,
	err error,
) {
	if cerr := ctx.Err(); cerr != nil {
		return nil, nil, fmt.Errorf("topology/iam_escalation: %w", cerr)
	}

	// Fan out: execute all rules in parallel. Each rule is read-only over
	// rctx (policyCache is RWMutex-protected) and returns independent edges.
	rules := allIAMRules()
	ruleResults := fanOutIAMRules(ctx, rctx, rules)

	// Sequential merge: stamp confidence/rule/account, apply boundary
	// filter, collect admin set. These write to shared maps.
	inferred = make(map[string][]iamInferredEdge)
	admins = make(map[string]bool)
	for i, name := range rules {
		if ruleResults[i].err != nil {
			return nil, nil, fmt.Errorf("topology/iam_escalation: rule %q: %w", name, ruleResults[i].err)
		}
		entry, ok := lookupIAMRuleEntry(name)
		if !ok {
			continue
		}
		stampAndMergeRuleEdges(ctx, rctx, entry, name, ruleResults[i].edges, inferred, admins)
	}
	// Also add principals attached to AdministratorAccess managed policy ARN.
	for _, p := range allPrincipals(rctx) {
		if hasAdminAttachment(ctx, rctx.scoped, p.Id) {
			admins[p.Id] = true
		}
	}
	return inferred, admins, nil
}

// iamRuleResult holds one rule's output from the parallel fan-out.
type iamRuleResult struct {
	edges []iamInferredEdge
	err   error
}

// fanOutIAMRules executes all IAM rules in parallel via sync.WaitGroup.
// Each rule reads from the same *iamRuleContext (whose policyCache is
// protected by an RWMutex) and writes only to its indexed result slot.
func fanOutIAMRules(ctx context.Context, rctx *iamRuleContext, rules []string) []iamRuleResult {
	results := make([]iamRuleResult, len(rules))
	var wg sync.WaitGroup
	wg.Add(len(rules))
	for i, name := range rules {
		entry, ok := lookupIAMRuleEntry(name)
		if !ok {
			wg.Done()
			continue
		}
		go func(idx int, e iamRuleEntry) {
			defer wg.Done()
			edges, rerr := e.Fn(ctx, rctx)
			results[idx] = iamRuleResult{edges: edges, err: rerr}
		}(i, entry)
	}
	wg.Wait()
	return results
}

// stampAndMergeRuleEdges applies boundary filtering, stamps
// confidence/rule/account on each edge, and merges into the shared
// inferred map and admin set. Must be called sequentially.
func stampAndMergeRuleEdges(
	ctx context.Context,
	rctx *iamRuleContext,
	entry iamRuleEntry,
	name string,
	edges []iamInferredEdge,
	inferred map[string][]iamInferredEdge,
	admins map[string]bool,
) {
	for _, e := range edges {
		if !edgeAllowedByBoundary(ctx, rctx, entry, e) {
			continue
		}
		if e.Confidence == 0 {
			e.Confidence = entry.Confidence
		}
		if e.RuleName == "" {
			e.RuleName = name
		}
		if e.Account == "" {
			e.Account = rctx.Account
		}
		if e.Kind == iamEdgeEffectiveAdmin || e.Kind == iamEdgeAttachPolicy {
			admins[e.FromID] = true
			continue
		}
		inferred[e.FromID] = append(inferred[e.FromID], e)
	}
}

// edgeAllowedByBoundary applies the permission-boundary gate to one rule
// output edge. Returns true (edge passes) when:
//
//   - the rule declared no Actions (entry.Actions == nil) — the rule is
//     not identity-gated and the boundary filter does not apply;
//   - the source principal cannot be resolved to a Node in the current
//     rule context (cross-account principal that only appears in another
//     account's graph) — the boundary is not visible here, fall through;
//   - the principal has no parseable boundary metadata — identity-only
//     evaluation wins, matching v1.1 behavior;
//   - the boundary allows EVERY action listed in entry.Actions against
//     resource "*". This is the strict AWS semantic.
//
// Returns false (edge dropped) when the principal IS resolvable AND has
// a parseable boundary AND the boundary does not allow at least one of
// the listed actions. This is the precise case the boundary filter
// exists to catch.
func edgeAllowedByBoundary(ctx context.Context, rctx *iamRuleContext, entry iamRuleEntry, e iamInferredEdge) bool {
	if len(entry.Actions) == 0 {
		return true
	}
	principal, ok := lookupPrincipalInContext(rctx, e.FromID)
	if !ok {
		return true
	}
	boundary := parseBoundaryPolicy(principal)
	if boundary == nil {
		return true
	}
	policies := iterPrincipalPolicies(ctx, rctx, principal)
	for _, action := range entry.Actions {
		if !actionAllowedWithinBoundary(principal, policies, action) {
			return false
		}
	}
	return true
}

// lookupPrincipalInContext returns the principal Node for the given ID
// from rctx.Users/Roles/Groups. Used by the boundary filter to resolve
// an edge's FromID back to the full Node so parseBoundaryPolicy can read
// the permission_boundary metadata. Returns ok=false for cross-account
// principals (they live in a different account's graph and are not
// enumerated in rctx).
func lookupPrincipalInContext(rctx *iamRuleContext, id string) (*knowledgev1.Node, bool) {
	for _, slice := range [][]*knowledgev1.Node{rctx.Users, rctx.Roles, rctx.Groups} {
		for i := range slice {
			if slice[i].Id == id {
				return slice[i], true
			}
		}
	}
	return nil, false
}

// hasAdminAttachment returns true if the principal has an outgoing
// EdgeGrants edge to the AdministratorAccess managed policy ARN.
func hasAdminAttachment(ctx context.Context, scoped *cloudReader, principalID string) bool {
	edges, _ := scoped.iterEdges(ctx, principalID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeGrants})
	for _, e := range edges {
		if e.ToId == "arn:aws:iam::aws:policy/AdministratorAccess" {
			return true
		}
	}
	return false
}

// buildSourceSet returns every principal that should be a BFS starting
// point: every non-admin principal in the rule context, plus every
// non-admin source ID found in the inferred edge map (cross-account ARNs
// won't appear in rctx.Users but DO appear as inferred edge sources).
func buildSourceSet(rctx *iamRuleContext, inferred map[string][]iamInferredEdge, admins map[string]bool) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(id string) {
		if id == "" || admins[id] || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, p := range allPrincipals(rctx) {
		add(p.Id)
	}
	for src := range inferred {
		add(src)
	}
	sort.Strings(out)
	return out
}

// sortEscalationFindings orders findings deterministically: shortest path
// first, then by primary evidence ID for stable tie-breaking.
func sortEscalationFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		li := findings[i].Metrics["hop_count"]
		lj := findings[j].Metrics["hop_count"]
		if li != lj {
			return li < lj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})
}

// humanKindLabel maps an iamInferredEdgeKind to a verb phrase for use in
// PMapper-style narrative Summary text. Unknown kinds fall back to a
// generic phrase so callers always produce readable output even when a
// new edge kind is added without updating this map.
//
// The labels are intentionally short and verbish so buildPMapperNarrative
// can stitch them into sentences of the form
// "<source> <verb phrase> <target>".
func humanKindLabel(kind iamInferredEdgeKind) string {
	switch kind {
	case iamEdgeAssumeRole:
		return "can assume role"
	case iamEdgeExecuteAs:
		return "can execute code as"
	case iamEdgeImpersonate:
		return "can impersonate"
	case iamEdgeAttachPolicy:
		return "can attach an admin-equivalent policy to"
	case iamEdgeEffectiveAdmin:
		return "is effective admin of"
	}
	return "can escalate to"
}

// init self-registers the analyzer.
func init() {
	Register(IAMEscalationAnalyzer{})
}
