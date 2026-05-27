// SPDX-License-Identifier: Apache-2.0

// event_chain.go implements EventChainAnalyzer — a cloud topology analyzer
// that traces event-driven processing chains to surface long chains,
// high fan-out sources, and circular event loops.
//
// The analyzer finds every event source node (EventBridge rule, SNS topic,
// Pub/Sub topic, Event Grid topic), then runs a forward BFS following
// event edges (TARGETS, TRIGGERS, SUBSCRIBES_TO, DEAD_LETTERS_TO, SINKS_TO)
// to measure chain length and fan-out.
//
// SEVERITY — based on chain length:
//
//   - chain_length >= 5 → Warning
//   - chain_length >= 3 → Notice
//   - else              → Info
//   - Circular loops    → Warning (always)
//
// CONFIGURATION via req.Extra:
//
//   - "max_depth" — BFS depth cap (default 15, max 100).
//
// DATA ACCESS — one foundation.FetchNodesByType(NodeCloudResource) browse
// supplies the candidate nodes; one bulk foundation.FetchEdges over the whole
// cloud node set (filtered to the event-chain edge types) supplies the
// adjacency the BFS walks in-memory. No per-node edge fetch.
package cloud

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// eventSourceTypes are resource_type values that identify event sources.
var eventSourceTypes = map[string]bool{
	"eventbridge-rule":      true,
	"sns-topic":             true,
	"gcp:pubsub:topic":      true,
	"azure-eventgrid:topic": true,
}

// eventChainEdges are the edge types the forward BFS follows.
var eventChainEdges = []kgtypes.EdgeType{
	kgtypes.EdgeTargets,
	kgtypes.EdgeTriggers,
	kgtypes.EdgeSubscribesTo,
	kgtypes.EdgeDeadLettersTo,
	kgtypes.EdgeSinksTo,
}

const (
	eventChainDefaultMaxDepth = 15
	eventChainWarningLength   = 5
	eventChainNoticeLength    = 3
)

// EventChainAnalyzer traces event-driven processing chains in cloud graphs.
// Zero-value usable; self-registers via init().
type EventChainAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (EventChainAnalyzer) Name() string { return "event_chain" }

// Run scopes to a single cloud account, finds event source nodes, and
// traces forward chains from each.
func (EventChainAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/event_chain: %w", err)
	}
	if req.Graph != kgtypes.GraphCloud {
		return nil, fmt.Errorf("topology/event_chain: requires GraphCloud, got %q", req.Graph)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/event_chain: req.Caller must not be nil")
	}

	nodes, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodeCloudResource)
	if err != nil {
		return nil, fmt.Errorf("topology/event_chain: fetch nodes cloud/%s: %w", req.Name, err)
	}

	maxDepth := extractExtraInt(req.Extra, "max_depth", eventChainDefaultMaxDepth, 100)
	sources := collectEventSources(nodes, req.Subset)
	if len(sources) == 0 {
		return nil, nil
	}

	idx, err := buildEventChainIndex(ctx, req, nodes)
	if err != nil {
		return nil, err
	}

	var findings []foundation.Finding
	for _, src := range sources {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("topology/event_chain: %w", cerr)
		}
		ff := eventChainBFS(idx, src, maxDepth)
		findings = append(findings, ff...)
	}

	sortEventChainFindings(findings)
	return foundation.TruncateTopK(findings, req.TopK), nil
}

// buildEventChainIndex fetches every event-chain edge incident to the cloud
// node set in ONE bulk read and returns the in-memory adjacency the BFS walks.
func buildEventChainIndex(ctx context.Context, req foundation.Request, nodes []*knowledgev1.Node) (*edgeIndex, error) {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			ids = append(ids, n.Id)
		}
	}
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, eventChainEdges)
	if err != nil {
		return nil, fmt.Errorf("topology/event_chain: fetch edges: %w", err)
	}
	return newEdgeIndex(edges), nil
}

func init() {
	foundation.Register(EventChainAnalyzer{})
}
