// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"fmt"
	"sort"
)

// k8s_reachability_matrix.go implements the raw-data emitter that turns the
// computed reachability index into a single "reachability_matrix" finding so
// downstream analyzers (notably public_exposure) can query the matrix by
// name+type rather than re-running the analyzer.
//
// CONTRACT. Exactly one matrix finding is emitted per normal classifier run.
// Emission is suppressed when the index is the skipped sentinel (the caller
// already emits a reachability_skipped notice in that path). The finding:
//
//   - Algorithm       = "k8s_reachability_matrix"
//   - Severity        = SeverityInfo
//   - Title           = "reachability_matrix"            (well-known name)
//   - Summary         = JSON-serialized []matrixEntry    (truncated at cap)
//   - Metadata keys   = "cluster", "pod_count", "entry_count"
//
// The well-known Title lets downstream consumers find the finding via
// Match(NodeFinding).Where(symbol_name == "reachability_matrix"); topology's
// EmitFindingsForGraph writes Title into the node's SymbolName column.
//
// TRUNCATION. The matrix is capped at matrixMaxEntries. When the raw matrix
// exceeds the cap, the emitted slice contains the first matrixMaxEntries
// entries followed by one sentinel entry whose Src == "truncated" noting the
// original total. This keeps the consumer side deterministic: the last
// entry is always either a real entry or the truncation sentinel.

// matrixMaxEntries bounds how many (src, dst, protocol, port, allowed)
// tuples the matrix finding carries. 10K is large enough for every realistic
// cluster at a handful of distinct ports, and small enough that JSON
// serialization stays well under 1MB.
const matrixMaxEntries = 10000

// matrixEntry is one row of the reachability matrix serialized into the
// matrix finding's Summary field. Field tags are stable because downstream
// consumers (public_exposure) decode them by name.
type matrixEntry struct {
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	Protocol string `json:"protocol,omitempty"`
	Port     int    `json:"port,omitempty"`
	Allowed  bool   `json:"allowed"`
	Note     string `json:"note,omitempty"`
}

// emitReachabilityMatrix builds and returns the single matrix finding for
// the given scoped cluster. Returns (zero, false) when the index is the
// skipped sentinel or has no pods — the caller treats (_, false) as "don't
// append this finding". Callers can opt out via req.Extra["emit_matrix"] =
// "false", which is useful on very large clusters where even the truncated
// matrix JSON is wasteful; the rest of the classifier still runs.
func emitReachabilityMatrix(req Request, index *reachabilityIndex, probes []portProbe) (Finding, bool) {
	if index == nil || index.skipped || len(index.pods) == 0 {
		return Finding{}, false
	}
	if req.Extra != nil {
		if v, ok := req.Extra["emit_matrix"]; ok && v == "false" {
			return Finding{}, false
		}
	}
	entries, total := buildMatrixEntries(index, probes)
	payload, err := json.Marshal(entries)
	if err != nil {
		// Never produce a broken finding — the test suite asserts on the
		// summary deserializing cleanly, and a malformed matrix would be
		// worse than none at all.
		return Finding{}, false
	}
	return Finding{
		Algorithm: "k8s_reachability_matrix",
		Severity:  SeverityInfo,
		Title:     "reachability_matrix",
		Summary:   string(payload),
		Evidence:  nil,
		Metrics: map[string]float64{
			"pod_count":   float64(len(index.pods)),
			"entry_count": float64(total),
		},
		Metadata: map[string]string{
			"cluster":     req.Name,
			"pod_count":   fmt.Sprintf("%d", len(index.pods)),
			"entry_count": fmt.Sprintf("%d", total),
		},
	}, true
}

// buildMatrixEntries walks the index and enumerates one entry per
// (src, dst, protocol, port) combination. Pods are iterated in sorted order
// so the serialized matrix is byte-stable across runs. Returns the entries
// slice (already truncated to matrixMaxEntries + 1 sentinel if needed) and
// the total count BEFORE truncation so the caller can surface it via
// Metadata.
func buildMatrixEntries(index *reachabilityIndex, probes []portProbe) ([]matrixEntry, int) {
	ids := sortedPodIDs(index)
	if len(probes) == 0 {
		probes = []portProbe{{}}
	}
	// Preallocate pessimistically for determinism; the upper bound is
	// len(ids)^2 * len(probes) but we cap at matrixMaxEntries+1.
	cap := matrixMaxEntries + 1
	entries := make([]matrixEntry, 0, cap)
	total := 0
	for _, src := range ids {
		for _, dst := range ids {
			if src == dst {
				continue
			}
			for _, probe := range probes {
				total++
				if len(entries) < matrixMaxEntries {
					entries = append(entries, matrixEntry{
						Src:      src,
						Dst:      dst,
						Protocol: probe.Protocol,
						Port:     probe.Port,
						Allowed:  index.canReach(src, dst, probe.Protocol, probe.Port),
					})
				}
			}
		}
	}
	if total > matrixMaxEntries {
		entries = append(entries, matrixEntry{
			Src:  "truncated",
			Dst:  "truncated",
			Note: fmt.Sprintf("matrix truncated at %d entries (original total %d)", matrixMaxEntries, total),
		})
	}
	// Final sort on (src, dst, protocol, port) keeps the serialized output
	// deterministic even if the iteration order above ever drifts.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Src != entries[j].Src {
			return entries[i].Src < entries[j].Src
		}
		if entries[i].Dst != entries[j].Dst {
			return entries[i].Dst < entries[j].Dst
		}
		if entries[i].Protocol != entries[j].Protocol {
			return entries[i].Protocol < entries[j].Protocol
		}
		return entries[i].Port < entries[j].Port
	})
	return entries, total
}
