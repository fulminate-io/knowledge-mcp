// SPDX-License-Identifier: Apache-2.0

package externalcollector

// summary_provenance.go carries one metadata key: the mark that says a node's
// summary came from its COLLECTOR rather than from anywhere else.
//
// WHY THE FACT NEEDS RECORDING AT ALL. The store DERIVES a summary for a node
// that arrives without one, copying its description into the summary field on the
// way in. So a stored node whose summary is non-empty could have got it two ways,
// and by the time the summary-gap scan looks, both look identical. The scan has a
// rule that depends on telling them apart — a collector's own summary is never
// re-written by the LLM summarizer, while a derived one is exactly what the
// summarizer exists to replace — and only the conversion site, holding the
// provider's raw output, still knows which happened.
//
// IT IS A KEY AND NOT A PROTO FIELD. Node metadata is an open string map that
// already crosses the wire, so nothing here moves a message or regenerates
// anything; the SERVER reads the same literal from its own copy, the way the LLM
// failure-marker keys already agree across these two modules.

import "maps"

// MetaKeyCollectorSummary marks a node whose summary the collector supplied.
//
// THE VALUE IS NOT READ, only the key's presence, so nothing downstream can start
// depending on a vocabulary of values that this side does not promise. "1" is
// written because an empty value ELIDES a metadata key on the store's write path,
// which would make the mark disappear on the way in.
const MetaKeyCollectorSummary = "collector_summary"

// collectorSummaryPresent is the value written under the key. See the const above
// for why it is not empty.
const collectorSummaryPresent = "1"

// withCollectorSummaryProvenance returns meta with the provenance mark set,
// COPYING rather than mutating: the map belongs to the caller's envelope, which
// the conversion does not own and a later reader may still be holding.
func withCollectorSummaryProvenance(meta map[string]string) map[string]string {
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the one provenance key costs at most a growth — while a size built by
	// addition is a value the allocator has to take on trust.
	out := make(map[string]string, len(meta))
	maps.Copy(out, meta)
	out[MetaKeyCollectorSummary] = collectorSummaryPresent
	return out
}
