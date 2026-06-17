// SPDX-License-Identifier: Apache-2.0

package pipeline

import "fmt"

// breakerAxis identifies one of the two independent pipeline breakers for the
// shared-cause escalation coordinator. Each axis owns its own circuitBreaker
// (summaryCircuit / embedCircuit); the coordinator reads the tripping axis and
// cross-trips the OTHER one when the shared-cause gate is met. (Named distinctly
// from the collector loop's loopAxis wiring, which is a separate concept.)
type breakerAxis int

const (
	summaryBreakerAxis breakerAxis = iota
	embedBreakerAxis
)

// label is the human-readable axis name embedded in a cross-trip reason.
func (a breakerAxis) label() string {
	if a == embedBreakerAxis {
		return "embed"
	}
	return "summary"
}

// escalateOnTrip is the shared-cause cross-axis coordinator. The worker calls it
// with the axis whose breaker JUST auto-tripped (recordErr returned tripped ==
// true). It is the single deliberate exception to per-axis independence: when one
// axis trips on a DOMINANT auth/quota error class AND both axes are bound to the
// SAME non-empty provider, the failing resource (that provider's quota /
// subscription / auth) is genuinely shared, so the OTHER axis is cross-tripped
// too. The provider-distinct case (summary='anthropic', embed='voyage') has
// DISTINCT providers, so the same-provider gate makes it NEVER cross-trip.
//
// It consumes the breaker's existing dominant-class API only: it READS the
// tripping axis's status() for the already-computed DominantClass (it does NOT
// recompute the class, and does NOT modify recordErr's narrow (class)->tripped
// contract). The cross-trip
// goes through the OTHER breaker's pause() — tripLocked is idempotent, so a
// breaker that is already paused is not double-paused (its reason is refreshed
// but pausedAt and parked waiters are undisturbed).
//
// Gated to AUTO-trip: it runs only off recordErr's tripped return, never off
// manual PausePipeline (which is already whole-pipeline). The cross-trip reason
// PRESERVES the tripping axis's verbatim status().Reason text and ADDS the axis
// label, so the operator sees the shared cause and which axis originated it.
func (p *Pipeline) escalateOnTrip(tripped breakerAxis) {
	src, other := p.summaryCircuit, p.embedCircuit
	if tripped == embedBreakerAxis {
		src, other = p.embedCircuit, p.summaryCircuit
	}
	st := src.status()
	if st.DominantClass != ClassAuthQuota {
		return
	}
	if p.summaryProvider == "" || p.summaryProvider != p.embedProvider {
		return
	}
	other.pause(fmt.Sprintf(
		"cross-tripped from %s axis (shared %s provider auth/quota failure): %s",
		tripped.label(), p.summaryProvider, st.Reason))
}

// PipelineStatus returns the current per-axis paused state for operator
// surfacing (pipeline_status manage op, search staleness footer). It reads BOTH
// breakers: each axis's full status() becomes a per-axis sub-state (Summary /
// Embed), and the top-level aggregate fields are taken from a representative
// paused axis (summary preferred when both are paused) so the existing
// footer/degraded paths that read only the aggregate keep working. It lives here
// (not pipeline.go) as the per-axis status counterpart to escalateOnTrip — both
// are cross-axis coordination over the two breakers.
func (p *Pipeline) PipelineStatus() PipelineStatus {
	summary := p.summaryCircuit.status()
	embed := p.embedCircuit.status()
	st := PipelineStatus{
		Paused:  summary.Paused || embed.Paused,
		Summary: circuitStatusToAxis(summary),
		Embed:   circuitStatusToAxis(embed),
	}
	// ActiveSummarizer is a SUMMARY-axis-only field that circuitStatus does not
	// carry — set it post-conversion from the wired callback (the fallback
	// chain's live active entry). nil callback (no chain / single entry / tests)
	// leaves it empty. The embed axis never has a summarizer entry.
	if p.activeSummarizer != nil {
		st.Summary.ActiveSummarizer = p.activeSummarizer()
	}
	// Aggregate top-level fields from a representative paused axis: summary when
	// it is paused (preferred), else embed when only embed is paused. When neither
	// is paused the aggregate stays zero-valued (Paused == false).
	rep := summary
	if !summary.Paused && embed.Paused {
		rep = embed
	}
	if st.Paused {
		st.Reason = rep.Reason
		st.Since = rep.Since
		st.DominantClass = rep.DominantClass
		st.DominantCount = rep.DominantCount
		st.Breakdown = rep.Breakdown
	}
	return st
}

// circuitStatusToAxis converts an in-package circuitStatus snapshot to the
// EXPORTED per-axis AxisStatus carrier. AxisStatus's first six fields mirror
// circuitStatus, but AxisStatus ALSO carries ActiveSummarizer (which
// circuitStatus lacks), so this is an explicit field-by-field construction
// rather than a direct AxisStatus(c) conversion — a struct conversion requires
// identical field sequences and would no longer compile. ActiveSummarizer is
// left zero here and set post-conversion in PipelineStatus.
func circuitStatusToAxis(c circuitStatus) AxisStatus {
	return AxisStatus{
		Paused:        c.Paused,
		Reason:        c.Reason,
		Since:         c.Since,
		DominantClass: c.DominantClass,
		DominantCount: c.DominantCount,
		Breakdown:     c.Breakdown,
	}
}
