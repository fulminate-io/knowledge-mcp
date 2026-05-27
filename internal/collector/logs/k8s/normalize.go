// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"maps"
	"strconv"
	"strings"
	"time"

	eventsv1 "k8s.io/api/events/v1"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// normalizeEvent converts a single events.k8s.io/v1 Event into one or two
// logwire.LogEntry values.
//
// Two-entry emission for series events is a deliberate choice: Kubernetes
// aggregates repeated occurrences of the "same" event (same Regarding /
// Reason / ReportingController) into a series Event whose EventTime marks
// when the series STARTED and whose Series.LastObservedTime marks the most
// recent occurrence. Emitting one entry per endpoint preserves the temporal
// SPAN of the burn so downstream clustering shows the range of activity,
// not just a single aggregation timestamp. Both entries share an identical
// Message (Reason + ": " + Note) so Drain clusters them into the same
// template, and both carry "count" as a label so filters and facets can
// surface the aggregation multiplier.
//
// Non-series events emit a single LogEntry at EventTime. Older API-server
// shims that don't populate EventTime (common on pre-1.25 shims projecting
// core/v1 Events into events.k8s.io/v1) fall back to
// DeprecatedLastTimestamp and then DeprecatedFirstTimestamp. As a final
// defense against a completely un-timestamped event we use time.Now() so
// the pipeline never sees a zero Timestamp.
func normalizeEvent(e *eventsv1.Event, kubeContext string) []logwire.LogEntry {
	if e == nil {
		return nil
	}

	severity := eventSeverity(e)
	message := eventMessage(e)
	labels := eventLabels(e, kubeContext)

	first, last := eventTimestamps(e)

	primary := logwire.LogEntry{
		Timestamp: first,
		Severity:  severity,
		Message:   message,
		Labels:    labels,
	}
	if last == nil {
		return []logwire.LogEntry{primary}
	}

	// Series events get a second entry at the most-recent observation.
	// Labels are duplicated (not shared) so downstream mutation of one
	// entry can't accidentally perturb the other.
	secondary := logwire.LogEntry{
		Timestamp: *last,
		Severity:  severity,
		Message:   message,
		Labels:    cloneLabels(labels),
	}
	return []logwire.LogEntry{primary, secondary}
}

// eventSeverity maps the K8s Event Type field to a canonical logs severity.
//
// "Warning" maps to ERROR, not WARN. In K8s, Warning events are failures
// an operator would page on (OOMKilled, FailedMount, BackOff, Unhealthy,
// FailedScheduling). Surfacing them as WARN buries them beneath pipeline
// noise; ERROR keeps them at the top of severity-filtered views.
//
// "Normal" and any other value (empty, unknown) map to INFO. logwire.ParseSeverity
// is intentionally NOT used here because the input is a closed K8s enum, not
// a free-form severity string — a small switch is clearer and catches
// future enum extensions at review time rather than silently degrading to
// INFO.
func eventSeverity(e *eventsv1.Event) string {
	switch e.Type {
	case "Warning":
		return logwire.SeverityError
	default:
		return logwire.SeverityInfo
	}
}

// eventMessage builds the LogEntry Message.
//
// Convention: "<Reason>: <Note>" so Drain's tokenizer clusters templates by
// Reason (the leading tokens are stable across instances) while still
// preserving the variable Note payload for example extraction. Reason is
// also emitted as a label (see eventLabels) so filters and facets don't
// need to parse the message.
//
// Edge cases:
//   - Empty Reason: return Note as-is.
//   - Empty Note: return Reason as-is (some Events carry only a Reason —
//     e.g. "Started" on lifecycle transitions).
//   - Both empty: return "" (the pipeline tolerates empty messages; Drain
//     treats them as a degenerate single-token template).
func eventMessage(e *eventsv1.Event) string {
	switch {
	case e.Reason == "":
		return e.Note
	case e.Note == "":
		return e.Reason
	default:
		return e.Reason + ": " + e.Note
	}
}

// eventTimestamps returns the primary timestamp and (for series events) the
// last-observed timestamp. For non-series events last is nil.
//
// Primary selection order: EventTime → DeprecatedLastTimestamp →
// DeprecatedFirstTimestamp → time.Now(). Series' LastObservedTime is used
// verbatim when set; if a series event somehow has a zero LastObservedTime
// we fall back to DeprecatedLastTimestamp (rare but observed on shims that
// project core/v1 aggregated Events).
func eventTimestamps(e *eventsv1.Event) (primary time.Time, last *time.Time) {
	primary = primaryEventTimestamp(e)
	if e.Series == nil {
		return primary, nil
	}
	ts := e.Series.LastObservedTime.Time
	if ts.IsZero() && !e.DeprecatedLastTimestamp.IsZero() {
		ts = e.DeprecatedLastTimestamp.Time
	}
	if ts.IsZero() {
		return primary, nil
	}
	return primary, &ts
}

// primaryEventTimestamp picks the best non-zero timestamp for a K8s event.
// Kept separate from eventTimestamps so both call sites (series and
// non-series) share the same fallback chain.
func primaryEventTimestamp(e *eventsv1.Event) time.Time {
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	if !e.DeprecatedLastTimestamp.IsZero() {
		return e.DeprecatedLastTimestamp.Time
	}
	if !e.DeprecatedFirstTimestamp.IsZero() {
		return e.DeprecatedFirstTimestamp.Time
	}
	// Last-resort: never emit a zero Timestamp — downstream chunk bounds
	// and stream time-range indexing rely on monotonic non-zero times.
	return time.Now()
}

// eventLabels constructs the LogEntry label map.
//
// Shape goals:
//   - `service`, `namespace`, `kind`, `pod_name`, `cluster_name`,
//     `project_id` match the keys the CloudResolver (tools/) reads from
//     log streams. Keeping this in sync is critical — a typo silently
//     disables cloud correlation.
//   - `reason`, `type`, `reporting_controller`, `reporting_instance`,
//     `related_kind`, `related_name`, `count` are the K8s-specific facets
//     operators expect to see in filters.
//   - Any label whose value is empty is omitted entirely so HighCard /
//     LowCard stream grouping doesn't see phantom "" values.
func eventLabels(e *eventsv1.Event, kubeContext string) map[string]string {
	labels := make(map[string]string, 12)

	setLabel(labels, "service", e.Regarding.Name)
	setLabel(labels, "namespace", e.Regarding.Namespace)
	setLabel(labels, "kind", e.Regarding.Kind)
	setLabel(labels, "reason", e.Reason)
	setLabel(labels, "type", e.Type)
	if e.Regarding.Kind == "Pod" {
		// Redundant with `service` for Pod events but kept because the
		// CloudResolver checks both keys — `pod_name` also triggers the
		// controller-derivation fallback that strips the -xxxx suffix to
		// recover the DaemonSet / Deployment name.
		setLabel(labels, "pod_name", e.Regarding.Name)
	}
	setLabel(labels, "reporting_controller", e.ReportingController)
	setLabel(labels, "reporting_instance", e.ReportingInstance)

	if e.Related != nil {
		setLabel(labels, "related_kind", e.Related.Kind)
		setLabel(labels, "related_name", e.Related.Name)
	}

	if e.Series != nil && e.Series.Count > 0 {
		labels["count"] = strconv.Itoa(int(e.Series.Count))
	}

	projectID, clusterName := parseKubeContext(kubeContext)
	setLabel(labels, "project_id", projectID)
	setLabel(labels, "cluster_name", clusterName)

	return labels
}

// setLabel writes a non-empty value into labels. Empty values are dropped
// so maps don't carry "" placeholders (stream fingerprinting treats
// "key="" as distinct from "key absent" and that distinction would split
// streams unnecessarily).
func setLabel(labels map[string]string, key, value string) {
	if value == "" {
		return
	}
	labels[key] = value
}

// cloneLabels returns an independent copy of labels so series entries don't
// share a map. Pre-sized to the source length to avoid rehashing.
func cloneLabels(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

// parseKubeContext extracts the GCP projectID and GKE cluster name from a
// kubeconfig context produced by `gcloud container clusters get-credentials`.
// The canonical shape is "gke_{project}_{region}_{cluster}" — project is
// the first segment after "gke_" and cluster is the remainder after the
// region. Contexts from EKS / AKS / on-prem yield ("", "") so the
// CloudResolver falls through to its "scan every loaded cloud graph"
// default.
//
// Design choice: we split on "_" and take [0] as project, [len-1] as
// cluster. Multi-segment cluster names (cluster names with underscores)
// are not supported — GKE cluster names are restricted to [a-z0-9-] so
// this is safe in practice and simpler than a regex.
func parseKubeContext(ctx string) (projectID, clusterName string) {
	const prefix = "gke_"
	if !strings.HasPrefix(ctx, prefix) {
		return "", ""
	}
	rest := ctx[len(prefix):]
	parts := strings.Split(rest, "_")
	// Need at least 3 tokens: project, region, cluster.
	if len(parts) < 3 {
		return "", ""
	}
	return parts[0], parts[len(parts)-1]
}
