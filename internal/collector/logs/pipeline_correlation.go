// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"context"
	"sort"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// defaultCorrelationWindow bounds how far apart two templates' time
// ranges may drift and still count as "co-occurring". Matches the
// brainstorm note: 60 seconds is tight enough to weed out unrelated
// background noise but loose enough to survive the timestamp drift
// between two services' log ingestors.
const defaultCorrelationWindow = 60 * time.Second

// findCorrelations scans error templates for pairs whose FirstSeen /
// LastSeen ranges overlap AND whose owning services differ, then gates
// each candidate against the DependencyChecker before marking it
// confirmed. When checker is nil every pair is returned unconfirmed so
// the summary still has material to present.
//
// Inputs:
//   - templates: full template list (non-error templates are filtered out)
//   - chunks: chunk list used to trace template → stream associations
//   - streams: stream list supplying service/namespace labels and the
//     cloud-context labels the resolver consults
//   - proxyMap: service-label value → "Account:ResourceID" diagnostic
//     string (populated by Pipeline.runCorrelations from the
//     wirelogs.ResolvedProxyEntry slice; may be empty). Used to populate
//     ResourceA/ResourceB on the result for diagnostics; also persists
//     in CORRELATES_WITH Edge.Evidence resources= field.
//   - resolver: optional CloudResolver used to map each candidate
//     template's service back to a ResolvedResource (Account+ID) so the
//     dependency checker can target the right cloud graph. Nil-safe.
//   - checker: optional dependency validator. Nil-safe.
//
// The returned slice is sorted by (ServiceA, ServiceB, TemplateA) for
// deterministic output.
func findCorrelations(
	ctx context.Context,
	templates []*wirelogs.LogTemplate,
	chunks []*wirelogs.LogChunk,
	streams []*wirelogs.LogStream,
	proxyMap map[string]string,
	resolver CloudResolver,
	checker DependencyChecker,
) []wirelogs.CorrelationResult {
	errorTemplates := filterErrorTemplates(templates)
	if len(errorTemplates) < 2 {
		return nil
	}
	tmplStreams := mapTemplateStreams(errorTemplates, chunks, streams)
	if len(tmplStreams) < 2 {
		return nil
	}
	tmplResources := resolveTemplateResources(ctx, tmplStreams, resolver)

	pairs := buildCandidatePairs(errorTemplates, tmplStreams)
	results := make([]wirelogs.CorrelationResult, 0, len(pairs))
	for _, p := range pairs {
		results = append(results, scoreCandidatePair(ctx, p, proxyMap, tmplResources, checker))
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].ServiceA != results[j].ServiceA {
			return results[i].ServiceA < results[j].ServiceA
		}
		if results[i].ServiceB != results[j].ServiceB {
			return results[i].ServiceB < results[j].ServiceB
		}
		return results[i].TemplateA < results[j].TemplateA
	})
	return results
}

// filterErrorTemplates keeps only templates at ERROR severity or above.
func filterErrorTemplates(templates []*wirelogs.LogTemplate) []*wirelogs.LogTemplate {
	out := make([]*wirelogs.LogTemplate, 0, len(templates))
	for _, t := range templates {
		if t == nil {
			continue
		}
		if !wirelogs.SeverityAtLeast(t.Severity, wirelogs.SeverityError) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// templateStreamRef bundles the owning stream with the service name we
// derived from it. Carrying the *wirelogs.LogStream lets resolveTemplateResources
// pass the right cloud-context labels (project_id, cluster_name, ...) to
// CloudResolver implementations.
type templateStreamRef struct {
	stream *wirelogs.LogStream
	svc    string
}

// mapTemplateStreams returns templateID → templateStreamRef. The service
// name is taken from the owning stream's label set (service or
// namespace), preferring service. Templates with no owning stream or no
// service label are skipped — they cannot participate in cross-service
// correlation.
func mapTemplateStreams(
	templates []*wirelogs.LogTemplate,
	chunks []*wirelogs.LogChunk,
	streams []*wirelogs.LogStream,
) map[string]templateStreamRef {
	errorIDs := make(map[string]struct{}, len(templates))
	for _, t := range templates {
		errorIDs[t.ID] = struct{}{}
	}

	streamByID := make(map[string]*wirelogs.LogStream, len(streams))
	for _, s := range streams {
		streamByID[s.ID] = s
	}

	tmplRef := make(map[string]templateStreamRef, len(templates))
	for _, c := range chunks {
		if _, keep := errorIDs[c.TemplateID]; !keep {
			continue
		}
		if _, known := tmplRef[c.TemplateID]; known {
			continue
		}
		s := streamByID[c.StreamID]
		if s == nil {
			continue
		}
		svc := serviceFromStream(s)
		if svc == "" {
			continue
		}
		tmplRef[c.TemplateID] = templateStreamRef{stream: s, svc: svc}
	}
	return tmplRef
}

// resolveTemplateResources resolves each template's owning service into
// a cloud-graph ResolvedResource via the supplied CloudResolver. A nil
// resolver yields an empty map; templates whose service does not
// resolve are absent from the returned map. Successful resolutions are
// cached per service-name so two templates sharing a service make a
// single resolver call.
func resolveTemplateResources(
	ctx context.Context,
	tmplStreams map[string]templateStreamRef,
	resolver CloudResolver,
) map[string]ResolvedResource {
	out := make(map[string]ResolvedResource, len(tmplStreams))
	if resolver == nil {
		return out
	}
	cache := make(map[string]ResolvedResource)
	missed := make(map[string]struct{})
	for tmplID, ref := range tmplStreams {
		if hit, ok := cache[ref.svc]; ok {
			out[tmplID] = hit
			continue
		}
		if _, miss := missed[ref.svc]; miss {
			continue
		}
		resolved, ok := resolver.ResolveService(ctx, ref.stream, ref.svc)
		if !ok {
			missed[ref.svc] = struct{}{}
			continue
		}
		cache[ref.svc] = resolved
		out[tmplID] = resolved
	}
	return out
}

// serviceFromStream returns the service-identifier for a stream. Prefers
// wirelogs.FieldService, falls back to wirelogs.FieldNamespace, then other service keys.
func serviceFromStream(s *wirelogs.LogStream) string {
	if v := s.Labels[wirelogs.FieldService]; v != "" {
		return v
	}
	if v := s.Labels[wirelogs.FieldNamespace]; v != "" {
		return v
	}
	if v := s.Labels["deployment"]; v != "" {
		return v
	}
	if v := s.Labels["app"]; v != "" {
		return v
	}
	return ""
}

// candidatePair is the intermediate shape passed around during
// correlation: two error templates, their services, and the overlap
// score. scoreCandidatePair converts it into a wirelogs.CorrelationResult.
type candidatePair struct {
	a, b     *wirelogs.LogTemplate
	svcA     string
	svcB     string
	overlap  float64
	overlaps bool
}

// buildCandidatePairs enumerates every pair of error templates owned by
// different services whose time ranges overlap. Self-pairs and
// same-service pairs are skipped so correlation is strictly a
// cross-service signal.
func buildCandidatePairs(
	templates []*wirelogs.LogTemplate,
	tmplStreams map[string]templateStreamRef,
) []candidatePair {
	pairs := make([]candidatePair, 0)
	for i := range templates {
		a := templates[i]
		refA, okA := tmplStreams[a.ID]
		if !okA {
			continue
		}
		for j := i + 1; j < len(templates); j++ {
			b := templates[j]
			refB, okB := tmplStreams[b.ID]
			if !okB || refA.svc == refB.svc {
				continue
			}
			score, overlap := temporalOverlap(a, b, defaultCorrelationWindow)
			if !overlap {
				continue
			}
			pairs = append(pairs, candidatePair{
				a: a, b: b, svcA: refA.svc, svcB: refB.svc,
				overlap: score, overlaps: true,
			})
		}
	}
	return pairs
}

// scoreCandidatePair upgrades a candidate to a wirelogs.CorrelationResult,
// calling HasDependency when a checker is wired in. The dependency
// check uses the per-template ResolvedResources so HasDependency
// targets the correct cloud graph (e.g., the GKE cluster graph rather
// than the parent GCP project graph).
func scoreCandidatePair(
	ctx context.Context,
	p candidatePair,
	proxyMap map[string]string,
	tmplResources map[string]ResolvedResource,
	checker DependencyChecker,
) wirelogs.CorrelationResult {
	res := wirelogs.CorrelationResult{
		TemplateA:         p.a.ID,
		TemplateB:         p.b.ID,
		ServiceA:          p.svcA,
		ServiceB:          p.svcB,
		ResourceA:         proxyMap[p.svcA],
		ResourceB:         proxyMap[p.svcB],
		CooccurrenceScore: p.overlap,
	}
	if checker == nil {
		return res
	}
	resA, okA := tmplResources[p.a.ID]
	resB, okB := tmplResources[p.b.ID]
	if !okA || !okB {
		return res
	}
	res.StructurallyConfirmed = checker.HasDependency(ctx, resA, resB)
	return res
}

// temporalOverlap returns a co-occurrence score and whether the two
// templates' time ranges overlap within window. The score is the ratio
// of shared interval length to the wider of the two ranges, clamped to
// [0, 1]. A window of 0 falls back to defaultCorrelationWindow.
//
// Zero-duration ranges (FirstSeen == LastSeen) are treated as point
// events: they overlap when the other range straddles them OR when the
// two points fall within window of each other.
//
//nolint:unparam // window is a tunable knob retained for future use
func temporalOverlap(a, b *wirelogs.LogTemplate, window time.Duration) (float64, bool) {
	if a == nil || b == nil {
		return 0, false
	}
	if window <= 0 {
		window = defaultCorrelationWindow
	}
	startA, endA := expandRange(a.FirstSeen, a.LastSeen, window)
	startB, endB := expandRange(b.FirstSeen, b.LastSeen, window)
	if endA.Before(startB) || endB.Before(startA) {
		return 0, false
	}
	overlapStart := startA
	if startB.After(overlapStart) {
		overlapStart = startB
	}
	overlapEnd := endA
	if endB.Before(overlapEnd) {
		overlapEnd = endB
	}
	overlap := overlapEnd.Sub(overlapStart).Seconds()
	wider := maxDuration(endA.Sub(startA), endB.Sub(startB)).Seconds()
	if wider <= 0 {
		return 1, true
	}
	score := overlap / wider
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score, true
}

// expandRange returns [first-window/2, last+window/2] so point events
// still intersect nearby ranges. Zero timestamps pass through unchanged.
func expandRange(first, last time.Time, window time.Duration) (time.Time, time.Time) {
	if first.IsZero() || last.IsZero() {
		return first, last
	}
	pad := window / 2
	return first.Add(-pad), last.Add(pad)
}

// maxDuration returns the larger of two durations.
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// BCN11.2 deleted writeCorrelations: the in-process flow no longer
// exists, so the DB-write helper is unreachable. CORRELATES_WITH edges
// are produced by cmd/knowledge/internal/collector/logs/materialize.go
// from the same CorrelationResult slice carried on CollectResult.
