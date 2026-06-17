// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"strings"
	"testing"
)

// driveSummaryAuthStorm trips the summary axis with a 3-call auth/quota
// (http_401 / ClassAuthQuota) zero-success round and returns the pipeline. Auth
// is NON-deterministic so it climbs the 3-call window rather than fast-tripping.
func driveSummaryAuthStorm(t *testing.T, summaryProvider, embedProvider string) *Pipeline {
	t.Helper()
	ctx := context.Background()
	wc := newFakeWireClient()
	fs := &fakeSummarizer{err: errTerminalNonDeterministic} // http_401 -> ClassAuthQuota
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{
		CircuitBreakerThreshold: 3,
		SummaryProvider:         summaryProvider,
		EmbedProvider:           embedProvider,
	}, wc, fs.call, fe.call)
	for range 3 {
		runSummaryWorkerBatch(ctx, p, []SummaryWork{summaryWork("s", `{"name":"s"}`)})
	}
	if !p.summaryCircuit.status().Paused {
		t.Fatalf("summary axis did not auto-trip on a 3-call auth/quota round")
	}
	if p.summaryCircuit.status().DominantClass != ClassAuthQuota {
		t.Fatalf("summary dominant class = %v, want ClassAuthQuota", p.summaryCircuit.status().DominantClass)
	}
	return p
}

// TestPipelineStatus_ActiveSummarizer asserts the live-active summarizer entry
// is surfaced in PipelineStatus().Summary.ActiveSummarizer: it reads the wired
// callback (which here reads a chainHealth) so it reports the primary's label
// before failover and the fallback's label after the primary is marked limited —
// the LIVE entry, not the static configured provider.
func TestPipelineStatus_ActiveSummarizer(t *testing.T) {
	wc := newFakeWireClient()
	fs := &fakeSummarizer{}
	fe := &fakeEmbedder{vectors: map[string][]byte{}}
	p := New(Config{}, wc, fs.call, fe.call)

	// Stand in for the chain's health + labels without importing llmproviders:
	// a tiny index+labels closure mirrors FallbackChain.ActiveEntry.
	active := 0
	labels := []string{"claude-cli/primary-model", "openai/fallback-model"}
	p.SetActiveSummarizer(func() string {
		if active < 0 || active >= len(labels) {
			return ""
		}
		return labels[active]
	})

	if got := p.PipelineStatus().Summary.ActiveSummarizer; got != "claude-cli/primary-model" {
		t.Errorf("before failover ActiveSummarizer = %q; want the primary", got)
	}
	active = 1 // simulate failover (chainHealth.MarkLimited(0) shifting ActiveIndex)
	if got := p.PipelineStatus().Summary.ActiveSummarizer; got != "openai/fallback-model" {
		t.Errorf("after failover ActiveSummarizer = %q; want the fallback", got)
	}
}

// TestEscalation_SameProviderAuthQuotaCrossTrips proves the lone deliberate
// cross-axis exception: when the summary axis auto-trips on an auth/quota window
// AND both axes share the SAME (non-empty) provider, the embed axis is
// cross-tripped too, with a reason naming the shared cause + originating axis.
func TestEscalation_SameProviderAuthQuotaCrossTrips(t *testing.T) {
	p := driveSummaryAuthStorm(t, "openai", "openai")

	embed := p.embedCircuit.status()
	if !embed.Paused {
		t.Fatalf("embed axis NOT cross-tripped despite same-provider auth/quota summary trip")
	}
	if !strings.Contains(embed.Reason, "cross-tripped") || !strings.Contains(embed.Reason, "openai") {
		t.Fatalf("cross-trip reason %q does not name the shared provider/cause", embed.Reason)
	}
	// The verbatim summary reason rides along in the cross-trip reason.
	if !strings.Contains(embed.Reason, p.summaryCircuit.status().Reason) {
		t.Fatalf("cross-trip reason %q does not preserve the originating summary reason", embed.Reason)
	}
}

// TestEscalation_DistinctProviderNeverCrossTrips is the provider-distinct case:
// summary='anthropic', embed='voyage'. A summary-axis auth/quota storm must NOT
// cross-trip the embed axis — distinct providers do not share a failing resource.
func TestEscalation_DistinctProviderNeverCrossTrips(t *testing.T) {
	p := driveSummaryAuthStorm(t, "anthropic", "voyage")

	if p.embedCircuit.status().Paused {
		t.Fatalf("embed axis cross-tripped on a DISTINCT-provider summary trip")
	}
}

// TestEscalation_EmptyProviderNeverCrossTrips proves an unknown (empty) provider
// identity never participates in escalation, even when both axes are empty (the
// test/degraded default) — the same-provider gate requires NON-empty equality.
func TestEscalation_EmptyProviderNeverCrossTrips(t *testing.T) {
	p := driveSummaryAuthStorm(t, "", "")

	if p.embedCircuit.status().Paused {
		t.Fatalf("embed axis cross-tripped despite empty provider identity (escalation must require non-empty)")
	}
}

// TestEscalation_ManualPauseDoesNotEscalate proves manual PausePipeline pauses
// BOTH axes via the manual (whole-pipeline) path, NOT the escalation path: even
// with same providers, a manual pause stamps the operator reason on both axes
// rather than a cross-trip reason.
func TestEscalation_ManualPauseDoesNotEscalate(t *testing.T) {
	wc := newFakeWireClient()
	p := New(Config{SummaryProvider: "openai", EmbedProvider: "openai"}, wc,
		(&fakeSummarizer{}).call, (&fakeEmbedder{vectors: map[string][]byte{}}).call)

	p.PausePipeline("manual operator pause")

	for axis, st := range map[string]circuitStatus{"summary": p.summaryCircuit.status(), "embed": p.embedCircuit.status()} {
		if !st.Paused {
			t.Fatalf("%s axis not paused after manual PausePipeline", axis)
		}
		if st.Reason != "manual operator pause" {
			t.Fatalf("%s axis reason = %q, want the verbatim manual reason (not a cross-trip reason)", axis, st.Reason)
		}
		if strings.Contains(st.Reason, "cross-tripped") {
			t.Fatalf("%s axis carries a cross-trip reason after a MANUAL pause", axis)
		}
	}
}
