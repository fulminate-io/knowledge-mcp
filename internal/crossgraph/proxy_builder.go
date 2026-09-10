// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// BuildCrossGraphProxy returns the fully-built proxy node for a deterministic
// cross-graph target (code, practice). It derives the proxy's
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
// The deterministic ID conventions (proxy:<repo>:<id>, proxy:practice:<lang>:<id>,
// and for every other named
// family proxy:custom/<type>:<name>:<id>) are byte-identical to the server-side
// store builder so client- and server-built proxy IDs match. Both sides are held
// to the checked-in vector at testdata/cross_graph_proxy_id_vector.json rather
// than to each other, so two builders drifting together is caught too.
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
	case string(kgtypes.GraphPractice):
		return buildPracticeProxy(target, proxy), nil
	case "", string(kgtypes.GraphKnowledge):
		return nil, fmt.Errorf(
			"BuildCrossGraphProxy: graph type %q has no deterministic ID convention; "+
				"use the server-side store.CreateCrossGraphProxy for generic / knowledge proxies",
			target.GetGraphType())
	default:
		return buildGenericProxy(target, proxy, kgtypes.NodeType(source.GetType()))
	}
}

// buildGenericProxy stamps the deterministic proxy for EVERY OTHER NAMED FAMILY
// — a registered custom graph type, and the built-in families that never got an
// arm of their own (logs, web, pdf, checks). Its id is
// "proxy:custom/<graphType>:<name>:<nodeID>" and its source is
// "proxy:custom/<graphType>:<name>".
//
// THE FAMILY SET IS OPEN BY DESIGN, which is why this is a default arm and not a
// fifth named case. Registration mints new graph types at runtime, so a builder
// that enumerated the families it would serve would be a list that goes stale
// every time an operator registers a collector — and a stale list here does not
// fail loudly, it drops a family back onto the erroring arm and breaks its
// cross-graph edges. The two families that are NOT served are handled above,
// deliberately and by name.
//
// WHY "custom/" IS IN THE SECOND SEGMENT AND WHY THAT SEGMENT. Without a family
// marker this id could equal the CODE arm's, which carries no family word: a
// custom family "jira" with graph "board-a" and node "X" and a code repo "jira"
// with node id "board-a:X" both render as "proxy:jira:board-a:X", and both are
// upserted into the same target graph, so the two foreign nodes would share one
// proxy. The SLASH is what makes that unreachable rather than merely unlikely:
// the code arm's second segment is a repository name, which is a filepath.Base
// product and cannot contain a slash, and the practice arm's second segment is
// the literal word "practice", which registration refuses as a custom family
// name. A family name may still contain a
// slash and that is harmless — this arm's second segment carries one either way.
// The colliding pair is a required row of the shared id vector.
//
// AN EMPTY GRAPH NAME IS REFUSED rather than collapsed into a shorter id. The
// caller reaches this arm having LOCATED the endpoint in a named graph, so an
// empty name means it located nothing; a two-segment fallback would silently
// merge every instance of a family onto one proxy. The practice arm's slug-less
// shape is a historical fallback for a scan that does not track which graph
// matched, and is deliberately not copied here.
func buildGenericProxy(target *knowledgev1.ProxyTarget, proxy *knowledgev1.Node, sourceType kgtypes.NodeType) (*knowledgev1.Node, error) {
	if target.GetName() == "" {
		return nil, fmt.Errorf(
			"BuildCrossGraphProxy: %s target requires Name (the graph instance the node was located in)",
			target.GetGraphType())
	}
	prefix := "proxy:custom/" + target.GetGraphType() + ":" + target.GetName()
	proxy.Id = prefix + ":" + target.GetNodeId()
	proxy.Source = prefix
	kgtypes.SetValue(proxy, "foreign_type", string(sourceType))
	return proxy, nil
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
