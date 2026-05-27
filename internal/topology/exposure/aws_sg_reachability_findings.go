// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// aws_sg_reachability_findings.go implements the classifier entry point
// that turns a sgReachabilityIndex into Finding values. The entry point
// classifyAWSSGReachability dispatches to sub-classifiers, the matrix
// emitter, and the skipped-sentinel handler. The sub-classifier
// implementations live in sibling files:
//
//   - aws_sg_reachability_findings_world.go  — world-open / wide-CIDR
//   - aws_sg_reachability_findings_chains.go — transitive chains / isolated
//
// SENTINEL HANDLING. When the index was short-circuited by the hard cap
// (index.skipped == true), classifyAWSSGReachability emits a single
// aws_sg_reachability_skipped notice finding and returns without running
// any sub-classifier.
//
// SEVERITY. Severities for 0.0.0.0/0 ingress findings are conditional on
// the attached resource_type and the exposed port. See
// severityForAttachment in aws_sg_reachability_severity.go.

// classifyAWSSGReachability is the entry point for the AWS SG reachability
// classifier. The skipped-sentinel path emits exactly one notice finding
// so operators know the analyzer ran but declined to build the full
// index. The normal path dispatches to each sub-classifier and returns
// their combined findings.
func classifyAWSSGReachability(
	ctx context.Context,
	req Request,
	index *sgReachabilityIndex,
) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/aws_sg_reachability: %w", err)
	}
	if index == nil {
		return nil, nil
	}
	if index.skipped {
		return []Finding{awsSGSkippedFinding(req, index)}, nil
	}

	results := fanOutSGClassifiers(req, index)

	findings := make([]Finding, 0, 16)
	for _, r := range results {
		findings = append(findings, r...)
	}
	return findings, nil
}

// fanOutSGClassifiers runs all 5 sub-classifiers as parallel goroutines,
// collects their results into indexed slots, and returns the merged array.
// Each classifier is read-only over *sgReachabilityIndex.
func fanOutSGClassifiers(req Request, index *sgReachabilityIndex) [5][]Finding {
	var results [5][]Finding
	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); results[0] = findWorldOpenPrivilegedPorts(index) }()
	go func() { defer wg.Done(); results[1] = findTransitiveSGChains(index) }()
	go func() { defer wg.Done(); results[2] = findWideCIDR(index) }()
	go func() { defer wg.Done(); results[3] = findIsolatedResources(index) }()
	go func() {
		defer wg.Done()
		if matrix, ok := emitSGReachabilityMatrix(req, index); ok {
			results[4] = []Finding{matrix}
		}
	}()
	wg.Wait()
	return results
}

// awsSGSkippedFinding is the single notice finding emitted when the index
// short-circuited on the hard resource cap.
func awsSGSkippedFinding(req Request, index *sgReachabilityIndex) Finding {
	title := "AWS SG reachability skipped: resource count exceeds hard cap"
	summary := fmt.Sprintf(
		"Account %q has %d SG-eligible resources, which exceeds the %d-resource hard cap for the aws_sg_reachability analyzer. The full reachability matrix was not built.",
		req.Name, index.resourceCount, sgReachabilityResourceCap,
	)
	return Finding{
		Algorithm: "aws_sg_reachability",
		Severity:  SeverityNotice,
		Title:     title,
		Summary:   summary,
		Metrics: map[string]float64{
			"resource_count": float64(index.resourceCount),
			"resource_cap":   float64(sgReachabilityResourceCap),
			"skipped_flag":   1,
		},
	}
}

// sortedResourceIDs returns the resource ARNs sorted lexicographically so
// classifier output is stable across runs.
func sortedResourceIDs(index *sgReachabilityIndex) []string {
	ids := make([]string, 0, len(index.resources))
	for id := range index.resources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// resourceLabel renders a resourceInfo as a short human-readable label
// for finding titles. Currently returns the raw ID; split out so future
// callers can switch to a cached SymbolName without touching every
// classifier.
func resourceLabel(res *resourceInfo) string {
	if res == nil {
		return ""
	}
	return res.ID
}
