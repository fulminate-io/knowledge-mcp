// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_v2_fixture_test.go is the Phase 10 acceptance test for the
// full v2 PMapper-style IAM escalation rule set. It builds a single cloud
// account whose principals collectively exercise every new rule at least
// once, runs the IAMEscalationAnalyzer against it, and asserts the end-to-end
// behavior: every escalation-capable principal produces at least one
// finding (or is classified as admin), the admin control principal produces
// no escalation finding, and the benign principal is a clean true negative.
//
// Fixture shape (single account 111111111111):
//
//   super    — IAM admin (AdministratorAccess attached). Control principal;
//              must NEVER appear as an escalation source.
//   alice    — iam:CreateLoginProfile             (impersonate rule)
//   bob      — iam:UpdateLoginProfile             (impersonate rule)
//   carol    — iam:PutUserPolicy                  (self-promotes → admin)
//   dave     — iam:AttachRolePolicy               (self-promotes → admin)
//   eve      — iam:CreatePolicyVersion + holds    (self-promotes → admin)
//              a customer-managed policy
//   frank    — iam:SetDefaultPolicyVersion +      (self-promotes → admin)
//              holds a customer-managed policy
//   grace    — iam:AddUserToGroup + admin group   (self-promotes → admin)
//              exists in account
//   heidi    — iam:UpdateAssumeRolePolicy on *    (execute_as to every role)
//   ivan     — lambda:UpdateFunctionCode + there  (execute_as to exec role)
//              is a Lambda function whose exec
//              role is effectively admin
//   judy     — lambda:UpdateFunctionConfiguration (execute_as to lambda role)
//              + iam:PassRole
//   ken      — iam:PassRole                       (execute_as to lambda/ec2
//              role via passRoleLambdaRule and
//              runInstancesRule)
//   leon     — cloudformation:CreateStack         (execute_as to cf role)
//   mallory  — iam:CreateAccessKey                (impersonate users rule)
//   nora     — NO policies                        (true negative — must not
//                                                   appear in any finding)
//
// Supporting resources:
//   - compute-admin role: trust policy names lambda.amazonaws.com,
//     ec2.amazonaws.com, cloudformation.amazonaws.com; has AdministratorAccess
//     attached; is the target of every compute PassRole / UpdateAssumeRolePolicy
//     edge.
//   - admins group: has an inline wildcard admin policy so the
//     addUserToGroup rule's groupReachesAdmin() check flips on for grace.
//   - custom-admin customer-managed policy: attached to eve and frank so
//     holdsCustomerManagedPolicy() flips true and the CreatePolicyVersion /
//     SetDefaultPolicyVersion self-loops fire.
//   - admin-fn lambda function with EdgeAssumesRole → compute-admin so
//     updateFunctionCodeRule has a (function, exec-role) pair to walk.
//
// The fixture lives in its own file (per Phase 10 Step 1 scoping) to keep
// iam_escalation_test.go from bloating further.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// v2 fixture ARNs — kept as constants so test assertions and the builder
// agree on identities.
const (
	v2Account    = accountA // 111111111111
	v2AdminRole  = "arn:aws:iam::111111111111:role/compute-admin"
	v2AdminGroup = "arn:aws:iam::111111111111:group/admins"
	v2CustomAdm  = "arn:aws:iam::111111111111:policy/custom-admin"
	v2LambdaFn   = "arn:aws:lambda:us-east-1:111111111111:function:admin-fn"

	v2Super   = "arn:aws:iam::111111111111:user/super"
	v2Alice   = "arn:aws:iam::111111111111:user/alice"
	v2Bob     = "arn:aws:iam::111111111111:user/bob"
	v2Carol   = "arn:aws:iam::111111111111:user/carol"
	v2Dave    = "arn:aws:iam::111111111111:user/dave"
	v2Eve     = "arn:aws:iam::111111111111:user/eve"
	v2Frank   = "arn:aws:iam::111111111111:user/frank"
	v2Grace   = "arn:aws:iam::111111111111:user/grace"
	v2Heidi   = "arn:aws:iam::111111111111:user/heidi"
	v2Ivan    = "arn:aws:iam::111111111111:user/ivan"
	v2Judy    = "arn:aws:iam::111111111111:user/judy"
	v2Ken     = "arn:aws:iam::111111111111:user/ken"
	v2Leon    = "arn:aws:iam::111111111111:user/leon"
	v2Mallory = "arn:aws:iam::111111111111:user/mallory"
	v2Nora    = "arn:aws:iam::111111111111:user/nora"
)

// v2SourcePrincipals lists every principal expected to produce an escalation
// finding as a source (i.e. is NOT already classified as admin by the v2
// rules). The Phase 10 test asserts each of these appears as the source of
// at least one finding.
var v2SourcePrincipals = []string{
	v2Alice, v2Bob, v2Heidi, v2Ivan, v2Judy, v2Ken, v2Leon, v2Mallory,
}

// v2SelfPromoters lists every principal expected to be admin via a self-loop
// rule (put_user_policy, attach_policy, create_policy_version,
// set_default_policy_version, add_user_to_group). These principals are
// architecturally "already admin" after rule dispatch, so the BFS excludes
// them as sources — the Phase 10 test instead verifies they are present in
// the admin set returned by dispatchIAMRules.
var v2SelfPromoters = []string{
	v2Carol, v2Dave, v2Eve, v2Frank, v2Grace,
}

// v2ExpectedRuleTokens lists every rule name the Phase 10 test expects to
// see in at least one finding's Evidence (as "rule:<name>" tokens). Only
// rules whose edges form non-self-loop paths end up in Evidence tokens; the
// self-promoter rules contribute through the admin set instead and are
// covered separately via the v2SelfPromoters admin-membership assertions.
var v2ExpectedRuleTokens = []string{
	"create_login_profile",
	"update_login_profile",
	"update_assume_role_policy",
	"update_function_code",
	"update_function_configuration",
	"pass_role_lambda",
	"run_instances",
	"cloudformation_create_stack",
	"create_access_key",
}

// buildV2Fixture constructs the Phase 10 v2 fixture and returns the
// populated cloudFixture rooted at a fresh in-memory store. The returned
// fixture contains exactly 15 principals (1 admin control, 13 escalators,
// 1 benign) plus the supporting resources (compute-admin role, admins
// group, custom-admin customer-managed policy, admin-fn Lambda function)
// that each rule needs to fire end-to-end.
//
// The builder is deliberately verbose rather than loop-driven so each
// principal's policy shape is locally visible next to its identity —
// debugging a mis-firing rule only requires reading the paragraph for that
// principal rather than chasing a table.
func buildV2Fixture(t *testing.T) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)

	// Supporting resources: admin role, admin group, custom-managed admin
	// policy, and the Lambda function whose exec role is compute-admin.
	addIAMRoleWithTrust(t, fx, v2Account, v2AdminRole, "compute-admin",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":["lambda.amazonaws.com","ec2.amazonaws.com","cloudformation.amazonaws.com"]},"Action":"sts:AssumeRole"}]}`)
	addAdminAttachment(t, fx, v2Account, v2AdminRole)

	// Admin group carries an inline wildcard admin so groupReachesAdmin()
	// returns true and grace's add_user_to_group rule self-promotes.
	addIAMGroup(t, fx, v2AdminGroup, "admins")
	addInlinePolicy(t, fx, v2AdminGroup, "admin",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)

	// Customer-managed admin policy — attached to eve and frank below so
	// holdsCustomerManagedPolicy() flips true and the CreatePolicyVersion /
	// SetDefaultPolicyVersion self-loops fire.
	addManagedPolicy(t, fx, v2Account, v2CustomAdm, "custom-admin",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)

	// Lambda function whose execution role is compute-admin. Exercised by
	// ivan (updateFunctionCodeRule) and judy (updateFunctionConfigurationRule
	// via passRoleServiceMultiActionRule).
	addLambdaFunction(t, fx, v2Account, v2LambdaFn, "admin-fn", v2AdminRole)

	// super — control principal: plain admin. Must NEVER appear as a source.
	addIAMUser(t, fx, v2Account, v2Super, "super")
	addAdminAttachment(t, fx, v2Account, v2Super)

	// alice — iam:CreateLoginProfile → impersonate edges to every user.
	addIAMUserWithInline(t, fx, v2Account, v2Alice, "alice", "login",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreateLoginProfile","Resource":"*"}]}`)

	// bob — iam:UpdateLoginProfile → impersonate edges to every user.
	addIAMUserWithInline(t, fx, v2Account, v2Bob, "bob", "login",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:UpdateLoginProfile","Resource":"*"}]}`)

	// carol — iam:PutUserPolicy → self-loop attach_policy → admin set.
	addIAMUserWithInline(t, fx, v2Account, v2Carol, "carol", "put",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:PutUserPolicy","Resource":"*"}]}`)

	// dave — iam:AttachRolePolicy → self-loop via attachPolicyRule → admin.
	addIAMUserWithInline(t, fx, v2Account, v2Dave, "dave", "attach",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:AttachRolePolicy","Resource":"*"}]}`)

	// eve — iam:CreatePolicyVersion. Also holds custom-admin (customer-
	// managed) so holdsCustomerManagedPolicy fires the self-loop branch.
	addIAMUserWithInline(t, fx, v2Account, v2Eve, "eve", "cpv",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreatePolicyVersion","Resource":"*"}]}`)
	attachPolicy(t, fx, v2Account, v2Eve, v2CustomAdm)

	// frank — iam:SetDefaultPolicyVersion + customer-managed policy held.
	addIAMUserWithInline(t, fx, v2Account, v2Frank, "frank", "sdpv",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:SetDefaultPolicyVersion","Resource":"*"}]}`)
	attachPolicy(t, fx, v2Account, v2Frank, v2CustomAdm)

	// grace — iam:AddUserToGroup. The admin group above makes
	// groupReachesAdmin true → attach_policy self-loop → admin set.
	addIAMUserWithInline(t, fx, v2Account, v2Grace, "grace", "addg",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:AddUserToGroup","Resource":"*"}]}`)

	// heidi — iam:UpdateAssumeRolePolicy on * → execute_as to every role
	// (including compute-admin which is pre-attached to AdministratorAccess).
	addIAMUserWithInline(t, fx, v2Account, v2Heidi, "heidi", "uarp",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:UpdateAssumeRolePolicy","Resource":"*"}]}`)

	// ivan — lambda:UpdateFunctionCode. The fixture's admin-fn has
	// compute-admin as its execution role, so updateFunctionCodeRule emits
	// ivan → compute-admin.
	addIAMUserWithInline(t, fx, v2Account, v2Ivan, "ivan", "ufc",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:UpdateFunctionCode","Resource":"*"}]}`)

	// judy — lambda:UpdateFunctionConfiguration + iam:PassRole together.
	// passRoleServiceMultiActionRule intersects both actions against the
	// set of roles trusting lambda.amazonaws.com → judy → compute-admin.
	addIAMUserWithInline(t, fx, v2Account, v2Judy, "judy", "ufcfg",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["lambda:UpdateFunctionConfiguration","iam:PassRole"],"Resource":"*"}]}`)

	// ken — iam:PassRole. Triggers passRoleLambdaRule (any role trusting
	// lambda) and runInstancesRule (any role trusting ec2). compute-admin
	// trusts both, so both rules emit ken → compute-admin and dedup merges
	// them into one finding with both rule tokens.
	addIAMUserWithInline(t, fx, v2Account, v2Ken, "ken", "pr",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`)

	// leon — cloudformation:CreateStack. compute-admin trusts
	// cloudformation.amazonaws.com so cloudformationCreateStackRule fires
	// without iam:PassRole (that rule does not require it — see
	// iam_rules_compute_passrole.go).
	addIAMUserWithInline(t, fx, v2Account, v2Leon, "leon", "cf",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudformation:CreateStack","Resource":"*"}]}`)

	// mallory — iam:CreateAccessKey → impersonate edges to every user.
	addIAMUserWithInline(t, fx, v2Account, v2Mallory, "mallory", "cak",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreateAccessKey","Resource":"*"}]}`)

	// nora — plain IAM user with no policies. True-negative control:
	// must not appear as a source, target, or intermediate in any finding.
	addIAMUser(t, fx, v2Account, v2Nora, "nora")

	return fx
}

// TestIAMEscalation_V2Fixture is the Phase 10 end-to-end acceptance test.
// It builds the full v2 fixture, runs IAMEscalationAnalyzer.Run against it,
// and asserts the end-to-end behavior across four axes:
//
//  1. Source coverage — every principal in v2SourcePrincipals produces at
//     least one escalation finding as source (alice, bob, heidi, ivan,
//     judy, ken, leon, mallory). That single assertion collectively
//     exercises eight v2 rules end-to-end.
//
//  2. Admin classification — every principal in v2SelfPromoters is
//     architecturally admin (carol, dave, eve, frank, grace). Admin
//     principals are excluded from the BFS source set, so they produce
//     no finding as source; the test instead re-runs dispatchIAMRules
//     directly and asserts their membership in the admin set. That
//     covers the other five v2 rules (put_user_policy, attach_policy,
//     create_policy_version, set_default_policy_version,
//     add_user_to_group) and the v1.1 wildcard_action rule (via super).
//
//  3. No false positives — the super control principal produces no
//     escalation finding and the nora benign principal does not appear
//     in any finding's Evidence at all (not as source, target, or
//     intermediate).
//
//  4. Finding quality — every finding has non-empty Summary (Phase 9
//     narrative), non-empty Evidence, positive min_confidence, and a
//     rule-token tail enumerating at least one contributing rule.
//
// Rule-token coverage — v2ExpectedRuleTokens lists every rule name
// expected to appear in at least one finding's Evidence rule-token tail.
// The test accumulates every rule token across every finding and asserts
// each expected token appears in the union. Self-promoter rules are NOT
// in this set by design (their edges are self-loops, never part of a
// reconstructed path).
func TestIAMEscalation_V2Fixture(t *testing.T) {
	fx := buildV2Fixture(t)
	ctx := newTestCtx(t)

	// Run the analyzer end-to-end. TopK=0 so every escalation path is
	// returned (we need to see all of them for the coverage assertions).
	findings := runAnalyzer(t, fx, v2Account, 0)
	require.NotEmpty(t, findings, "v2 fixture must surface at least one escalation finding")

	// The lower bound is one finding per source-producing principal; the
	// upper bound is a generous ceiling that allows the 1:N path fan-out
	// (each source reaches multiple admin terminals) without overspecifying
	// the exact count and coupling the test to dedup internals.
	assert.GreaterOrEqual(t, len(findings), len(v2SourcePrincipals),
		"expected at least one finding per source-producing principal (got %d)", len(findings))
	assert.LessOrEqual(t, len(findings), 80,
		"finding count ceiling (got %d) — dedup regression likely if this trips", len(findings))

	// (1) Source coverage — every source-producing principal must be the
	// Evidence[0] of at least one finding.
	sourcesSeen := map[string]bool{}
	for _, f := range findings {
		require.NotEmpty(t, f.Evidence, "finding Evidence must not be empty")
		sourcesSeen[f.Evidence[0]] = true
	}
	for _, p := range v2SourcePrincipals {
		assert.Truef(t, sourcesSeen[p],
			"expected escalation finding sourced from %s, got sources %v", p, sortedKeys(sourcesSeen))
	}

	// (3a) No-false-positive on super: must never appear as a source.
	assert.False(t, sourcesSeen[v2Super],
		"super is pre-existing admin and must not appear as an escalation source")

	// (3b) nora must not appear anywhere in any finding's Evidence — she
	// has no policies so every rule should skip her cleanly. This catches
	// both source-classification bugs (nora treated as escalator) and
	// target-classification bugs (nora reached as a stepping stone).
	for _, f := range findings {
		for _, ev := range f.Evidence {
			assert.NotEqualf(t, v2Nora, ev,
				"nora must not appear in any finding Evidence (got %+v)", f.Evidence)
		}
	}

	// (4) Finding quality — every finding carries a narrative Summary, a
	// non-empty Evidence list, a positive min_confidence metric, and
	// terminates in AdministratorAccess per the Phase 9 narrative builder.
	for i := range findings {
		f := findings[i]
		assert.NotEmptyf(t, f.Summary, "finding[%d] must have Phase 9 narrative Summary", i)
		assert.Containsf(t, f.Summary, terminalAdminState,
			"finding[%d] narrative must end in %q", i, terminalAdminState)
		assert.Equalf(t, SeverityCritical, f.Severity, "finding[%d] must be Critical", i)
		assert.Greaterf(t, f.Metrics["min_confidence"], 0.0,
			"finding[%d] min_confidence must be positive", i)
		assert.GreaterOrEqualf(t, f.Metrics["hop_count"], 1.0,
			"finding[%d] must have at least one hop", i)
	}

	// (Rule coverage) — union every rule token across every finding's
	// Evidence. Each expected rule must appear in at least one finding.
	seenRules := collectRuleTokens(findings)
	for _, want := range v2ExpectedRuleTokens {
		assert.Truef(t, seenRules[want],
			"expected rule token %q in at least one finding's Evidence (got %v)", want, sortedKeys(seenRules))
	}

	// (2) Admin classification — re-run dispatchIAMRules directly so we
	// can introspect the admin set. The self-promoter rules contribute
	// via self-loops that the BFS consumes as admin markers before the
	// walk begins, so they never surface in Evidence tokens; this is the
	// only way to verify the five remaining rules fired.
	rctx := newTestRuleContext(t, fx, v2Account)
	inferred, admins, err := dispatchIAMRules(ctx, rctx)
	require.NoError(t, err)
	require.NotNil(t, inferred)

	assert.Truef(t, admins[v2Super], "super must be classified admin (AdministratorAccess attached)")
	for _, p := range v2SelfPromoters {
		assert.Truef(t, admins[p],
			"self-promoter %s must be in admin set after rule dispatch", p)
	}
	assert.Falsef(t, admins[v2Nora], "nora must not be classified admin (no policies)")
}

// collectRuleTokens returns the union of every rule-token tail across
// every finding. Rule tokens are the Evidence entries prefixed with
// "rule:" appended by Phase 9 Step 3 dedup; they enumerate every rule
// that contributed an edge along or parallel to the path.
func collectRuleTokens(findings []Finding) map[string]bool {
	out := map[string]bool{}
	const prefix = "rule:"
	for _, f := range findings {
		for _, ev := range f.Evidence {
			if len(ev) > len(prefix) && ev[:len(prefix)] == prefix {
				out[ev[len(prefix):]] = true
			}
		}
	}
	return out
}

// sortedKeys returns the keys of a bool-map sorted alphabetically — used
// only in assertion failure messages so the test output lists observed
// sources/rules deterministically regardless of map iteration order.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Intentionally use a tiny hand-rolled sort rather than importing
	// sort here to keep this file's import list minimal.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// Compile-time reference to the store package. Keeps the import alive
// even if the test body is shrunk in a future edit; the fixture builder
// already uses store transitively via the helpers.
var _ = kgtypes.GraphCloud
