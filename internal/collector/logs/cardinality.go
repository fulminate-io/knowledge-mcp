// SPDX-License-Identifier: Apache-2.0

package logs

// DefaultCardinalityThreshold is the maximum number of unique values a label
// key can have before it is considered high-cardinality.
const DefaultCardinalityThreshold = 500

// CardinalityTracker counts unique values per label key and classifies keys
// as low-cardinality (shared graph nodes) or high-cardinality (stored inline).
type CardinalityTracker struct {
	counts    map[string]map[string]struct{}
	threshold int
}

// NewCardinalityTracker creates a tracker with the given threshold.
// If threshold <= 0, DefaultCardinalityThreshold is used.
func NewCardinalityTracker(threshold int) *CardinalityTracker {
	if threshold <= 0 {
		threshold = DefaultCardinalityThreshold
	}
	return &CardinalityTracker{
		counts:    make(map[string]map[string]struct{}),
		threshold: threshold,
	}
}

// Observe records a value for a label key.
func (ct *CardinalityTracker) Observe(key, value string) {
	vals, ok := ct.counts[key]
	if !ok {
		vals = make(map[string]struct{})
		ct.counts[key] = vals
	}
	vals[value] = struct{}{}
}

// IsLowCardinality returns true if the key has fewer unique values than the
// threshold. Keys that have never been observed are treated as low-cardinality.
func (ct *CardinalityTracker) IsLowCardinality(key string) bool {
	return len(ct.counts[key]) < ct.threshold
}

// Classify splits a label set into low-cardinality and high-cardinality maps
// based on observed cardinality. Low-cardinality labels become shared graph
// nodes; high-cardinality labels are stored inline on the stream node.
func (ct *CardinalityTracker) Classify(labels map[string]string) (lowCard, highCard map[string]string) {
	lowCard = make(map[string]string, len(labels))
	highCard = make(map[string]string)
	for k, v := range labels {
		if ct.IsLowCardinality(k) {
			lowCard[k] = v
		} else {
			highCard[k] = v
		}
	}
	return lowCard, highCard
}
