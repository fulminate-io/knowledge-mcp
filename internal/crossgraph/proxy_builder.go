// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// BuildCrossGraphProxy returns the fully-built proxy node for a deterministic
// cross-graph target (code, cloud, cicd, practice). It derives the proxy's
// deterministic ID + Source + metadata from target and source WITHOUT touching
// the DB. This is the client-side relocation of the pure half of the former
// store.CreateCrossGraphProxy, retyped over the proto carrier so the client
// emits proxy nodes through a single batch-write/upsert path instead of one
// Upsert per proxy.
//
// target is the proto knowledgev1.ProxyTarget — its graph_type field carries the
// kgtypes.GraphType string value. Generic (knowledge / version) targets are NOT
// supported by this helper; those use auto-generated IDs which require a live DB
// (the server retains store.CreateCrossGraphProxy for that case).
//
// The deterministic ID conventions (proxy:<repo>:<id>, proxy:cloud:<acct>:<id>,
// proxy:cicd:<acct>:<id>, proxy:practice:<lang>:<id>) are byte-identical to the
// server-side store builder so client- and server-built proxy IDs match.
func BuildCrossGraphProxy(target *knowledgev1.ProxyTarget, source *knowledgev1.Node) (*knowledgev1.Node, error) {
	if target.GetNodeId() == "" {
		return nil, fmt.Errorf("BuildCrossGraphProxy: target NodeID is required")
	}

	proxy := newProxyBase(source)

	// Set metadata common to all proxy types.
	if target.GetGraphType() != "" {
		kgtypes.SetValue(proxy, "foreign_graph", target.GetGraphType())
	}
	kgtypes.SetValue(proxy, "foreign_id", target.GetNodeId())

	switch target.GetGraphType() {
	case string(kgtypes.GraphCode):
		return buildCodeProxy(target, proxy, kgtypes.NodeType(source.GetType()))
	case string(kgtypes.GraphCloud):
		return buildCloudProxy(target, proxy, source)
	case string(kgtypes.GraphCICD):
		return buildCICDProxy(target, proxy, kgtypes.NodeType(source.GetType()))
	case string(kgtypes.GraphPractice):
		return buildPracticeProxy(target, proxy), nil
	default:
		return nil, fmt.Errorf(
			"BuildCrossGraphProxy: graph type %q has no deterministic ID convention; "+
				"use the server-side store.CreateCrossGraphProxy for generic / knowledge proxies",
			target.GetGraphType())
	}
}

// newProxyBase initializes a proxy node from source, copying the lightweight
// fields (SymbolName, FilePath, Description, Language) and falling back to
// Summary for an empty Description. The returned node is a fresh *knowledgev1.Node
// allocated by composite literal — never a value-copy of a populated proto (which
// would trip govet copylocks on the embedded MessageState lock).
func newProxyBase(source *knowledgev1.Node) *knowledgev1.Node {
	proxy := &knowledgev1.Node{
		Type:        string(kgtypes.NodeProxy),
		SymbolName:  source.GetSymbolName(),
		FilePath:    source.GetFilePath(),
		Description: source.GetDescription(),
		Language:    source.GetLanguage(),
	}
	if source.GetSummary() != "" && proxy.GetDescription() == "" {
		proxy.Description = source.GetSummary()
	}
	return proxy
}

// buildCodeProxy stamps a deterministic code-graph proxy. target.Name is the
// repo (required); proxy ID format is "proxy:<repo>:<nodeID>" — the missing
// "code:" flavor prefix is historical compatibility with the server
// codegraph/routing.go convention.
func buildCodeProxy(target *knowledgev1.ProxyTarget, proxy *knowledgev1.Node, sourceType kgtypes.NodeType) (*knowledgev1.Node, error) {
	if target.GetName() == "" {
		return nil, fmt.Errorf("BuildCrossGraphProxy: code target requires Name (repo)")
	}
	proxy.Id = "proxy:" + target.GetName() + ":" + target.GetNodeId()
	proxy.Source = "proxy:" + target.GetName()
	kgtypes.SetValue(proxy, "repo", target.GetName())
	kgtypes.SetValue(proxy, "foreign_type", string(sourceType))
	return proxy, nil
}

// buildCloudProxy stamps a deterministic cloud-graph proxy and copies a fixed
// set of cloud display metadata (resource_type, region, provider) so proxy
// consumers can render without re-resolving against the cloud graph.
func buildCloudProxy(target *knowledgev1.ProxyTarget, proxy *knowledgev1.Node, source *knowledgev1.Node) (*knowledgev1.Node, error) {
	if target.GetName() == "" {
		return nil, fmt.Errorf("BuildCrossGraphProxy: cloud target requires Name (account)")
	}
	proxy.Id = "proxy:cloud:" + target.GetName() + ":" + target.GetNodeId()
	proxy.Source = "proxy:cloud:" + target.GetName()
	kgtypes.SetValue(proxy, "account", target.GetName())
	kgtypes.SetValue(proxy, "foreign_type", source.GetType())
	if rt := kgtypes.Value(source, "resource_type"); rt != "" {
		kgtypes.SetValue(proxy, "resource_type", rt)
	}
	if region := kgtypes.Value(source, "region"); region != "" {
		kgtypes.SetValue(proxy, "region", region)
	}
	if provider := kgtypes.Value(source, "provider"); provider != "" {
		kgtypes.SetValue(proxy, "provider", provider)
	}
	return proxy, nil
}

// buildCICDProxy stamps a deterministic CI/CD-graph proxy. Mirrors the cloud
// branch: target.Name is the account/org slug. Without this case CICD proxies
// would fall through to a fresh-ID-per-link path, defeating dedup.
func buildCICDProxy(target *knowledgev1.ProxyTarget, proxy *knowledgev1.Node, sourceType kgtypes.NodeType) (*knowledgev1.Node, error) {
	if target.GetName() == "" {
		return nil, fmt.Errorf("BuildCrossGraphProxy: cicd target requires Name (account)")
	}
	proxy.Id = "proxy:cicd:" + target.GetName() + ":" + target.GetNodeId()
	proxy.Source = "proxy:cicd:" + target.GetName()
	kgtypes.SetValue(proxy, "account", target.GetName())
	kgtypes.SetValue(proxy, "foreign_type", string(sourceType))
	return proxy, nil
}

// buildPracticeProxy is the practice branch of BuildCrossGraphProxy. Practice
// proxies use a deterministic ID so repeated link calls reuse the same proxy
// instead of stamping a new one each time. target.Name is the practice language
// slug (e.g. "go", "go-idioms"); empty is permitted for callers that scan loaded
// graphs without tracking which one matched — the foreign_id alone is unique
// enough for those, and the slug-less shape is preserved as a fallback so
// existing callers don't break.
func buildPracticeProxy(target *knowledgev1.ProxyTarget, proxy *knowledgev1.Node) *knowledgev1.Node {
	if target.GetName() != "" {
		proxy.Id = "proxy:practice:" + target.GetName() + ":" + target.GetNodeId()
		proxy.Source = "proxy:practice:" + target.GetName()
		kgtypes.SetValue(proxy, "language", target.GetName())
	} else {
		proxy.Id = "proxy:practice:" + target.GetNodeId()
		proxy.Source = "proxy:practice"
	}
	return proxy
}
