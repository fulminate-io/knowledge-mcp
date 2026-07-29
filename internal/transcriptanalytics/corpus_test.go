// SPDX-License-Identifier: Apache-2.0

package transcriptanalytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// TestHistPercentile pins the histogram-quantile primitive (the same method the platform's
// server-side usage analytics uses): the q-quantile is bucketRepresentative of the bucket
// where the cumulative count first reaches ceil(q*total).
func TestHistPercentile(t *testing.T) {
	assert.Equal(t, int64(1023), histPercentile(map[int]int64{9: 2}, 2, 0.5), "{bucket9:2} p50 → rep(9)=2^10-1")
	assert.Equal(t, int64(4095), histPercentile(map[int]int64{11: 1}, 1, 0.9), "{bucket11:1} p90 → rep(11)=2^12-1")
	// Cumulative walk across buckets: total 3, p90 ceil(2.7)=3 lands in the higher bucket.
	assert.Equal(t, int64(4095), histPercentile(map[int]int64{9: 2, 11: 1}, 3, 0.9), "cumulative walk to bucket 11")
	assert.Equal(t, int64(1023), histPercentile(map[int]int64{9: 2, 11: 1}, 3, 0.5), "p50 ceil(1.5)=2 stops in bucket 9")
	assert.Equal(t, int64(0), histPercentile(map[int]int64{}, 0, 0.5), "empty → 0")
}

// TestBucketRepresentative pins the frozen bucket upper-edge 2^(b+1)-1.
func TestBucketRepresentative(t *testing.T) {
	assert.Equal(t, int64(1023), bucketRepresentative(9))
	assert.Equal(t, int64(4095), bucketRepresentative(11))
	assert.Equal(t, int64(511), bucketRepresentative(8))
}

// TestDurationBucket pins the log2-bucket boundary math + the 31 clamp — the SAME table as
// transcriptsync/rollup_test.go. LOCKSTEP with the sync wire contract: a one-sided change
// to latencyBucketMaxExp / the bucket scheme here or in transcriptsync desyncs client/cloud
// percentiles and MUST fail this test.
func TestDurationBucket(t *testing.T) {
	assert.Equal(t, 0, durationBucket(0), "0 → 0")
	assert.Equal(t, 0, durationBucket(-5), "negative → 0")
	assert.Equal(t, 0, durationBucket(1), "1ms → bucket 0")
	assert.Equal(t, 1, durationBucket(2), "2ms → bucket 1")
	assert.Equal(t, 1, durationBucket(3), "3ms → bucket 1")
	assert.Equal(t, 9, durationBucket(1000), "1000ms → bucket 9")
	assert.Equal(t, 11, durationBucket(3000), "3000ms → bucket 11")
	assert.Equal(t, latencyBucketMaxExp, durationBucket(1<<40), "huge → clamped to 31")
	assert.Equal(t, 31, latencyBucketMaxExp, "the clamp constant is 31 (transcriptsync/wire.go:163)")
}

// TestTrustworthy pins the idle-guard predicate at its boundaries (mirror of
// transcriptsync rollupTrustworthy).
func TestTrustworthy(t *testing.T) {
	assert.True(t, trustworthy(transcripts.Row{DurationMs: 1}), "1ms trustworthy")
	assert.True(t, trustworthy(transcripts.Row{DurationMs: idleGuardCeilingMs}), "ceiling trustworthy")
	assert.False(t, trustworthy(transcripts.Row{DurationMs: 0}), "0ms not trustworthy")
	assert.False(t, trustworthy(transcripts.Row{DurationMs: idleGuardCeilingMs + 1}), "over-ceiling not trustworthy")
	assert.False(t, trustworthy(transcripts.Row{DurationMs: 100, Interrupted: true}), "interrupted not trustworthy")
}

// TestFiltersKeep pins the baseline predicate: is_meta==true and synthetic-model rows are
// dropped, a MISSING/false is_meta is KEPT (the CEO fix), and the field filters apply.
func TestFiltersKeep(t *testing.T) {
	var f Filters
	assert.True(t, f.keep(transcripts.Row{Model: "m"}), "plain row kept (is_meta false)")
	assert.False(t, f.keep(transcripts.Row{Model: "m", IsMeta: true}), "is_meta==true dropped")
	assert.False(t, f.keep(transcripts.Row{Model: syntheticModel}), "synthetic-model dropped")

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	since := Filters{Since: base}
	assert.False(t, since.keep(transcripts.Row{Model: "m", RecordTS: base.Add(-time.Second)}), "before Since dropped")
	assert.True(t, since.keep(transcripts.Row{Model: "m", RecordTS: base}), "at Since kept (inclusive)")
	until := Filters{Until: base}
	assert.False(t, until.keep(transcripts.Row{Model: "m", RecordTS: base}), "at Until dropped (exclusive)")
	assert.True(t, until.keep(transcripts.Row{Model: "m", RecordTS: base.Add(-time.Second)}), "before Until kept")

	model := Filters{Model: "x"}
	assert.False(t, model.keep(transcripts.Row{Model: "m"}), "model filter mismatch dropped")
	assert.True(t, model.keep(transcripts.Row{Model: "x"}), "model filter match kept")
	tool := Filters{Tool: "Bash"}
	assert.False(t, tool.keep(transcripts.Row{Model: "m", ToolName: "Read"}), "tool filter mismatch dropped")
	proj := Filters{Project: "/w"}
	assert.False(t, proj.keep(transcripts.Row{Model: "m", Project: "/other"}), "project filter mismatch dropped")
}
