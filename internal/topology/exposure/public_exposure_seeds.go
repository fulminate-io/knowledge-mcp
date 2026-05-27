// SPDX-License-Identifier: Apache-2.0

package exposure

// public_exposure_seeds.go owns the public-entry seed registry and
// enumeration dispatcher for the public_exposure analyzer family. A "seed"
// is a cloud resource that the walker should start BFS from: internet-
// facing load balancers, Lambda function URLs with public auth, S3 buckets
// with public-access-block disabled, EC2 instances with a public IP, etc.
//
// Sibling files (public_exposure_seeds_aws.go, public_exposure_seeds_k8s.go)
// register per-resource-type rules via init() calls. Each rule knows how
// to re-parse the relevant slice of node.Content (the collector's JSON
// envelope) and decide whether the resource is actually public.
//
// The cloud filter lets each analyzer wrapper (aws_public_exposure,
// k8s_public_exposure, unified_public_exposure) narrow the seed set to
// the resources it cares about without forcing the walker to rewrite
// its scoping logic.
//
// LAYERING. topology/ must not import cloud/ — every rule re-parses raw
// node.Content into a LOCAL anonymous struct. The resource_type strings
// are the only stable cross-package contract (documented in the Phase 1
// seed catalog finding).

import (
	"context"
	"fmt"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// publicSeed describes one detected public-entry point. The walker starts
// BFS from NodeID using EntryScore as the initial sensitivity weight. The
// Reason is copied into the finding narrative so operators see WHY a seed
// was flagged as public ("Lambda function URL with AUTH_TYPE=NONE",
// "internet-facing ALB", "S3 bucket with PAB disabled", ...).
type publicSeed struct {
	// NodeID is the cloud resource node ID the BFS walker starts from.
	NodeID string
	// ResourceType is the collector-written resource_type string for this
	// node. Carried so the finding summary can render "Lambda function
	// foo is publicly exposed" rather than a generic "resource ... is
	// publicly exposed" line.
	ResourceType string
	// EntryScore is the initial per-seed weight in [0, 1]. Not every
	// public-facing resource is equally dangerous: a Lambda URL with
	// AUTH_TYPE=NONE is a direct code-execution surface (score 1.0),
	// while an EC2 instance with just a public IP and no open SGs is
	// much less severe (score ~0.5).
	EntryScore float64
	// Reason is a short human-readable phrase used by the scoring module
	// when rendering the finding summary. Must never be empty for a
	// returned seed.
	Reason string
	// CloudFamily is "aws", "k8s", "azure", or "gcp" — the family the
	// rule belongs to. Used by enumerateSeeds when a caller filters by
	// cloud family.
	CloudFamily string
}

// seedRule decides whether a single node is a public-entry seed. Returns
// (nil, nil) for non-seeds (the common case) and (seed, nil) for seeds.
// Parse errors on malformed content should return (nil, nil) — a missing
// field is "not public" not "error" so a partially-populated graph never
// blocks enumeration.
type seedRule func(ctx context.Context, scoped *cloudReader, node *knowledgev1.Node) (*publicSeed, error)

// seedRuleEntry packages a rule with its declared cloud family. Kept
// together so enumerateSeeds can filter by family without re-inspecting
// each rule's output.
type seedRuleEntry struct {
	CloudFamily string
	Rule        seedRule
}

// seedRegistry is the global resource_type → rule lookup. Populated
// exclusively from sibling-file init() blocks. Panics on duplicate
// registration.
var (
	seedRegistryMu sync.RWMutex
	seedRegistry   = map[string]seedRuleEntry{}
)

// registerSeedRule adds a rule to the registry keyed by resource_type.
// The cloudFamily argument must be one of the strings used by the cloud-
// filter parameter of enumerateSeeds ("aws", "k8s", "azure", "gcp").
// Panics on duplicate registration, nil rule, empty resourceType, or
// unknown cloudFamily — all programmer errors.
func registerSeedRule(resourceType, cloudFamily string, rule seedRule) {
	if resourceType == "" {
		panic("topology: registerSeedRule called with empty resourceType")
	}
	if rule == nil {
		panic(fmt.Sprintf("topology: registerSeedRule(%q) called with nil rule", resourceType))
	}
	switch cloudFamily {
	case "aws", "k8s", "azure", "gcp":
		// valid
	default:
		panic(fmt.Sprintf("topology: registerSeedRule(%q) invalid cloudFamily %q (want aws, k8s, azure, or gcp)", resourceType, cloudFamily))
	}
	seedRegistryMu.Lock()
	defer seedRegistryMu.Unlock()
	if _, dup := seedRegistry[resourceType]; dup {
		panic(fmt.Sprintf("topology: duplicate seed rule registration: %q", resourceType))
	}
	seedRegistry[resourceType] = seedRuleEntry{CloudFamily: cloudFamily, Rule: rule}
}

// enumerateSeeds walks the scoped cloud-graph listing cloud-resource nodes
// and invokes the matching seed rule on each. cloudFilter is "aws",
// "k8s", or "" (all). Returns seeds in deterministic order (sorted by
// NodeID) so callers can rely on stable output across runs.
//
// Errors from individual rules are suppressed by design — a single
// collector bug must not take down the whole enumeration pass. The
// deliberate contract is: parse-failures are "not a seed", full stop.
// Rule authors that want to surface parse errors should log them via
// slog and return (nil, nil) so enumeration continues.
func enumerateSeeds(ctx context.Context, scoped *cloudReader, cloudFilter string) []publicSeed {
	if scoped == nil {
		return nil
	}
	nodes, err := scoped.cloudResources(ctx)
	if err != nil {
		return nil
	}
	var out []publicSeed
	for _, n := range nodes {
		if err := ctx.Err(); err != nil {
			return out
		}
		resourceType := nodeMeta(n, "resource_type")
		if resourceType == "" {
			continue
		}
		seedRegistryMu.RLock()
		entry, ok := seedRegistry[resourceType]
		seedRegistryMu.RUnlock()
		if !ok {
			continue
		}
		if cloudFilter != "" && entry.CloudFamily != cloudFilter {
			continue
		}
		seed, rerr := entry.Rule(ctx, scoped, n)
		if rerr != nil {
			// Preserve the legacy IterateAll semantic: a rule error halts
			// the enumeration pass (returning whatever was accumulated so
			// far). Seed rules are documented never to error in practice
			// (parse-failures are "not a seed"), so this only fires on a
			// genuine ctx cancellation surfaced through a rule.
			break
		}
		if seed == nil {
			continue
		}
		seed.CloudFamily = entry.CloudFamily
		if seed.NodeID == "" {
			seed.NodeID = n.Id
		}
		if seed.ResourceType == "" {
			seed.ResourceType = resourceType
		}
		out = append(out, *seed)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}
