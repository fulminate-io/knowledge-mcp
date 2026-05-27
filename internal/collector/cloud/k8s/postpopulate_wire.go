// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// proxyAccumulator collects the cross-graph proxy nodes a K8s resolver
// materializes so they can be emitted in the SAME create_batch as the edges
// that reference them (postpopulate.LinkNodesAndEdgesBatch). It replaces the
// per-resolver CreateCrossGraphProxy + db.Upsert calls: the wire path has
// no in-process store, so a proxy node must travel in the create_batch payload
// rather than being upserted out-of-band before the edge batch.
//
// Proxies are deduped by deterministic ID (crossgraph.BuildCrossGraphProxy is
// idempotent for cloud/cicd/code targets) so two resolvers (or two edges) that
// reference the same upstream node contribute exactly one node body.
//
// The accumulated bodies are typed *knowledgev1.Node (the wire payload type
// LinkNodesAndEdgesBatch consumes), produced directly by the relocated
// crossgraph.BuildCrossGraphProxy builder over the proto node/target.
type proxyAccumulator struct {
	byID map[string]*knowledgev1.Node
}

func newProxyAccumulator() *proxyAccumulator {
	return &proxyAccumulator{byID: map[string]*knowledgev1.Node{}}
}

// proxy builds (and remembers) a deterministic cross-graph proxy for target,
// returning its ID. The proxy node is added to the accumulator so the caller
// can emit it via LinkNodesAndEdgesBatch. Returns ("", err) when the proxy
// cannot be built (e.g. missing target NodeId).
//
// source is a display-only *knowledgev1.Node seed built fresh by the
// per-resolver *ProxySource helpers; crossgraph.BuildCrossGraphProxy returns the
// owned *knowledgev1.Node proxy directly, which we store in the accumulator.
func (a *proxyAccumulator) proxy(target *knowledgev1.ProxyTarget, source *knowledgev1.Node) (string, error) {
	node, err := crossgraph.BuildCrossGraphProxy(target, source)
	if err != nil {
		return "", err
	}
	a.byID[node.GetId()] = node
	return node.GetId(), nil
}

// nodes returns the accumulated proxy nodes (deterministic order is irrelevant
// — create_batch upserts by ID).
func (a *proxyAccumulator) nodes() []*knowledgev1.Node {
	if len(a.byID) == 0 {
		return nil
	}
	out := make([]*knowledgev1.Node, 0, len(a.byID))
	for _, n := range a.byID {
		out = append(out, n)
	}
	return out
}
