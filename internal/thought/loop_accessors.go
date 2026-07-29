// SPDX-License-Identifier: Apache-2.0

package thought

// loop_accessors.go holds the p.mu-guarded reads of the propagation loop's cached
// tick output — clusters, personality profile, tensions, blind spots — plus the
// synchronous cold-cache trigger the on-demand handlers use. Split out of loop.go
// to keep that file under the 500-line cap; these are pure accessors over state
// loop.go owns, with no pass logic of their own.

// GetClusters returns the most recently detected clusters and
// personality profile. Both may be nil if cluster detection has not
// run yet.
func (p *PropagationLoop) GetClusters() ([]ThoughtCluster, *PersonalityProfile) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastClusters, p.lastProfile
}

// GetBlindSpots returns the most recently computed faceted blind-spot report.
// A zero-value report (Computed=false) is the cold sentinel returned before the
// propagation loop has completed a tick (including right after a daemon restart);
// the on-demand handler reads Computed to render the not-yet-computed message.
// This is the seam the blind_spots handler serves the cache through — p.mu-guarded,
// mirroring GetClusters.
func (p *PropagationLoop) GetBlindSpots() BlindSpotReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastBlindSpots
}

// GetClustersCached returns the most recently detected clusters + personality
// profile from the loop tick, plus a `computed` flag that is false until the
// loop has stored at least one tick (the cold sentinel). Mirrors GetBlindSpots'
// Computed-false cold path. Distinct from GetClusters() (kept unchanged for its
// similarity_lever callers) only by carrying the cold flag.
func (p *PropagationLoop) GetClustersCached() ([]ThoughtCluster, *PersonalityProfile, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastClusters, p.lastProfile, p.lastComputed
}

// GetTensions returns the most recently computed tension reports plus the cold
// flag (false before the first tick). Mirrors GetBlindSpots.
func (p *PropagationLoop) GetTensions() ([]TensionReport, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastTensions, p.lastComputed
}

// TriggerClusterDetection runs cluster detection synchronously and
// caches the result. Used by client-side reflective handlers when the
// cache is cold.
func (p *PropagationLoop) TriggerClusterDetection() {
	if p == nil {
		return
	}
	p.runClusterDetection()
}
