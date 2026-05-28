// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_sensitive.go owns the sensitive-terminal classifier registry
// for the public_exposure analyzer family (aws/k8s/unified). Callers use
// classifySensitive to test whether a given cloud graph node is a "terminal"
// worth reaching — e.g. a managed database, a secret, an admin IAM role.
//
// Rules are registered per resource_type string from sibling files
// (public_exposure_sensitive_aws.go, public_exposure_sensitive_k8s.go) via
// init() calls. Registration is static and must be idempotent across
// init() ordering: duplicate registrations panic per the Go convention used
// elsewhere in topology/ (see registry.go Register).
//
// The classifier contract (OQ5 decision): TYPE-BASED and finding-lookup ONLY.
// No name regex, no tag matching, no label heuristics — those are fragile
// and tend to drift from collector reality. An iam-role is "sensitive" if
// and only if a persisted iam_escalation finding targets it; a Kubernetes
// ServiceAccount is sensitive only if it has an IRSA annotation pointing at
// an admin-reachable IAM role. Everything else is either "always sensitive"
// for its type (rds-instance, kms-key, secretsmanager-secret, ...) or not
// sensitive at all.
//
// LAYERING. topology/ must not import cloud/ — rules read raw node content
// and metadata via the *knowledgev1.Node surface only. Rules MAY consume
// persisted Findings from the knowledge graph by issuing a meta-filtered
// knowledge-findings wire read; they MUST NOT invoke other Analyzers at
// runtime.

import (
	"context"
	"fmt"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// SensitiveRule evaluates whether a given node is a sensitive terminal and
// returns (ok, score, reason). score is in [0, 1] and reflects how
// catastrophic reaching this node would be: 0.9 for "relational database",
// 1.0 for "admin IAM role", etc. reason is a short human-readable phrase
// used by the scoring module in the Finding summary text.
//
// Rules MUST return (false, 0, "") for non-matches and (true, >0, "...")
// for matches. Implementations receive both:
//
//   - scoped:  the cloud-graph reader the walker is iterating over (for
//     reading the node's edges or related cloud-side resources).
//   - rootCaller: the unscoped wire caller (for reaching the knowledge graph
//     via foundation.FetchKnowledgeFindings — needed by rules that consume
//     persisted analyzer findings like iam_escalation).
//
// rootCaller may be nil when the caller is running in a reduced context
// (tests); rules that require it must guard for nil and return
// (false, 0, "") in that case.
type SensitiveRule func(ctx context.Context, scoped *cloudReader, rootCaller foundation.GraphCaller, node *knowledgev1.Node) (sensitive bool, score float64, reason string)

// sensitiveRegistry is the global resource_type → rule lookup used by
// classifySensitive. It is populated exclusively from init() blocks in
// sibling files. Protected by an RWMutex because init() ordering is not
// guaranteed to be serial in the Go runtime contract even if it is in
// practice today.
var (
	sensitiveRegistryMu sync.RWMutex
	sensitiveRegistry   = map[string]SensitiveRule{}
)

// registerSensitiveRule adds a rule to the registry keyed by resource_type.
// Panics on duplicate registration or nil rule — both are programmer
// errors, not runtime conditions. The panic convention matches Register()
// in registry.go.
func registerSensitiveRule(resourceType string, rule SensitiveRule) {
	if resourceType == "" {
		panic("topology: registerSensitiveRule called with empty resourceType")
	}
	if rule == nil {
		panic(fmt.Sprintf("topology: registerSensitiveRule(%q) called with nil rule", resourceType))
	}
	sensitiveRegistryMu.Lock()
	defer sensitiveRegistryMu.Unlock()
	if _, dup := sensitiveRegistry[resourceType]; dup {
		panic(fmt.Sprintf("topology: duplicate sensitive rule registration: %q", resourceType))
	}
	sensitiveRegistry[resourceType] = rule
}

// classifySensitive dispatches to the registered rule for node's
// resource_type. Returns (false, 0, "") for unknown resource types so the
// walker treats unknown terminals as "not sensitive" (the safe default —
// false negatives are better than noisy false positives).
//
// The function is safe for concurrent callers: it takes an RLock on the
// registry and the rule itself is expected to be a pure function of its
// inputs. rootCaller may be nil in reduced-context callers (tests) — rules
// that require it must guard for nil internally.
func classifySensitive(ctx context.Context, scoped *cloudReader, rootCaller foundation.GraphCaller, node *knowledgev1.Node) (bool, float64, string) {
	resourceType := nodeMeta(node, "resource_type")
	if resourceType == "" {
		return false, 0, ""
	}
	sensitiveRegistryMu.RLock()
	rule, ok := sensitiveRegistry[resourceType]
	sensitiveRegistryMu.RUnlock()
	if !ok {
		return false, 0, ""
	}
	return rule(ctx, scoped, rootCaller, node)
}
