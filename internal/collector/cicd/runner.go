// SPDX-License-Identifier: Apache-2.0

package cicd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// RunOptions configures a RunSubCollectors call.
type RunOptions struct {
	// OnProgress is called after each subcollector completes.
	// current is the 1-based index, total is len(subs).
	OnProgress collector.ProgressFunc

	// MaxConcurrency limits parallel subcollector goroutines.
	// 0 or negative means default (10).
	MaxConcurrency int
}

const defaultConcurrency = 10

// subResult holds the output of a single subcollector goroutine.
type subResult struct {
	name  string
	nodes []*knowledgev1.Node
	edges []kgwire.BatchEdge
	err   error
}

// RunSubCollectors iterates all subcollectors in parallel, calls each one's
// Collect, and merges the results into flat slices of nodes and edges.
//
// Error handling is best-effort: if a subcollector returns an error the error
// is recorded (wrapped with the subcollector name) but execution continues
// with the remaining subcollectors. All collected errors are joined and
// returned alongside any partial results.
//
// If the context is cancelled, in-flight subcollectors see it via ctx and
// pending ones are skipped.
func RunSubCollectors(
	ctx context.Context,
	subs []SubCollector,
	opts RunOptions,
) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	if len(subs) == 0 {
		return nil, nil, nil
	}

	concurrency := opts.MaxConcurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}

	total := len(subs)
	results := make([]subResult, total)
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var completed atomic.Int32

	for i, sub := range subs {
		if err := ctx.Err(); err != nil {
			results[i] = subResult{name: sub.Name(), err: err}
			break
		}

		wg.Add(1)
		go func(idx int, s SubCollector) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			name := s.Name()
			result, err := s.Collect(ctx)
			if err != nil {
				results[idx] = subResult{name: name, err: fmt.Errorf("subcollector %s: %w", name, err)}
				done := int(completed.Add(1))
				slog.Warn("cicd subcollector failed", "name", name, "done", done, "total", total, "error", err)
				reportProgress(opts.OnProgress, done, total, fmt.Sprintf("%s: error", name))
				return
			}

			nodes, edges := convertResult(result)
			results[idx] = subResult{
				name:  name,
				nodes: nodes,
				edges: edges,
			}
			done := int(completed.Add(1))
			slog.Info("cicd subcollector done", "name", name, "done", done, "total", total, "nodes", len(nodes))
			reportProgress(opts.OnProgress, done, total, name)
		}(i, sub)
	}

	wg.Wait()
	return mergeResults(results)
}

// convertResult converts a SubCollectorResult into graph types.
func convertResult(result SubCollectorResult) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	var nodes []*knowledgev1.Node
	for _, res := range result.Resources {
		nodes = append(nodes, BuildNode(res))
	}
	var edges []kgwire.BatchEdge
	for _, e := range result.Edges {
		edges = append(edges, BuildEdge(e))
	}
	return nodes, edges
}

// mergeResults combines subResult slices into flat output, collecting errors.
func mergeResults(results []subResult) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	var (
		allNodes []*knowledgev1.Node
		allEdges []kgwire.BatchEdge
		errs     []error
	)
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		allNodes = append(allNodes, r.nodes...)
		allEdges = append(allEdges, r.edges...)
	}
	return allNodes, allEdges, errors.Join(errs...)
}

// reportProgress calls the progress callback if non-nil.
func reportProgress(fn collector.ProgressFunc, current, total int, msg string) {
	if fn != nil {
		fn(current, total, msg)
	}
}
