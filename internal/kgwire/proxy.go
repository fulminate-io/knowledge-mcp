// SPDX-License-Identifier: Apache-2.0

package kgwire

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// IsProxy reports whether n is a proxy node (a lightweight reference to a node
// in another graph). Mirrors store.IsProxy
// (cmd/knowledge-server/internal/store/proxy.go:20), retyped to
// read the *knowledgev1.Node wire form via kgtypes instead of the server-side
// *store.Node wrapper. Pure field read.
func IsProxy(n *knowledgev1.Node) bool {
	return n != nil && kgtypes.NodeType(n.GetType()) == kgtypes.NodeProxy
}

// IsBranchProxy reports whether n is a branch overlay proxy — a hollowed node
// whose real content lives in the base (main) graph. Branch proxies carry
// foreign_graph="main" metadata. Mirrors store.IsBranchProxy
// (cmd/knowledge-server/internal/store/proxy.go:27).
func IsBranchProxy(n *knowledgev1.Node) bool {
	return IsProxy(n) && kgtypes.Value(n, "foreign_graph") == "main"
}

// ProxyInfo extracts the cross-graph target reference from a proxy node's
// metadata. Mirrors store.ProxyInfo
// (cmd/knowledge-server/internal/store/proxy.go:44) byte-for-byte: the
// same five-pattern switch (main/code/cloud/practice + repo / foreign_id
// fallbacks), reading via kgtypes.Value over the *knowledgev1.Node wire type.
// Returns the proto *knowledgev1.ProxyTarget data carrier (graph_type carries
// the kgtypes.GraphType string value). Returns nil when n is not a proxy or
// carries no recognizable proxy metadata.
//
// The five proxy patterns and their metadata conventions:
//
//   - Branch:   foreign_graph="main", overlay=<name>            -> same ID in base graph
//   - Code:     foreign_graph="code", repo=<r>, foreign_id=<id>  -> code graph <r>
//   - Cloud:    foreign_graph="cloud", account=<a>, foreign_id=<id> -> cloud graph <a>
//   - Practice: foreign_graph="practice", foreign_id=<id>        -> a practice graph
//   - Version:  foreign_id=<id> (no foreign_graph)               -> knowledge graph
//
// Cross-repo code proxies (created by codegraph routing) use repo + foreign_id
// without foreign_graph; they are detected by the presence of repo metadata.
func ProxyInfo(n *knowledgev1.Node) *knowledgev1.ProxyTarget {
	if !IsProxy(n) {
		return nil
	}
	switch kgtypes.Value(n, "foreign_graph") {
	case "main":
		// Overlay proxy — same ID in the base graph.
		return &knowledgev1.ProxyTarget{Name: kgtypes.Value(n, "overlay"), NodeId: n.GetId()}
	case "code":
		// Dream-analyze code symbol proxy.
		return &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), Name: kgtypes.Value(n, "repo"), NodeId: kgtypes.Value(n, "foreign_id")}
	case "cloud":
		// Cloud resource proxy.
		return &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCloud), Name: kgtypes.Value(n, "account"), NodeId: kgtypes.Value(n, "foreign_id")}
	case "practice":
		// Dream-analyze best-practice proxy.
		return &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphPractice), NodeId: kgtypes.Value(n, "foreign_id")}
	}
	if repo := kgtypes.Value(n, "repo"); repo != "" {
		// Cross-repo code proxy (created by codegraph/routing.go).
		return &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphCode), Name: repo, NodeId: kgtypes.Value(n, "foreign_id")}
	}
	if fid := kgtypes.Value(n, "foreign_id"); fid != "" {
		// Version overlay proxy — references a knowledge graph node.
		return &knowledgev1.ProxyTarget{GraphType: string(kgtypes.GraphKnowledge), NodeId: fid}
	}
	return nil
}
