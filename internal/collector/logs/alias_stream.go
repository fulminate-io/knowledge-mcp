// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"sort"
	"strings"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// AliasFor returns a readable identifier for a stream derived from its
// label set. The shape depends on the inferred provider:
//
//   - K8s events:  <object>.<reason>             e.g. api-7b6.OOMKilled
//   - Loki:        <app>@<instance>              e.g. checkout@host-3
//   - Stackdriver: <host|pod>.<resource_type>    e.g. api-7b6.k8s_container
//   - CloudWatch:  <log_stream>.<service>        e.g. ecs-task.api-server
//   - Generic:     <key1>=<val1>.<key2>=<val2>   first two labels by key
//
// Returns the empty string only when the label set is empty AND no
// fingerprint is set; callers should fall back to a hash-derived locator
// in that case (the engine layer takes care of this).
func AliasFor(stream *wirelogs.LogStream) string {
	if stream == nil {
		return ""
	}
	labels := stream.Labels
	if len(labels) == 0 {
		labels = stream.LowCardLabels
	}
	if len(labels) == 0 {
		return ""
	}
	switch inferProvider(labels) {
	case providerK8s:
		return aliasK8s(labels)
	case providerLoki:
		return aliasLoki(labels)
	case providerStackdriver:
		return aliasStackdriver(labels)
	case providerCloudWatch:
		return aliasCloudWatch(labels)
	default:
		return aliasGeneric(labels)
	}
}

// provider enumerates the recognized label shapes. The zero value is
// providerGeneric so anything that doesn't match a known shape falls
// through to the generic deriver.
type provider int

const (
	providerGeneric provider = iota
	providerK8s
	providerLoki
	providerStackdriver
	providerCloudWatch
)

// inferProvider classifies a label set by checking for distinctive keys.
// The order of checks matters because some labels overlap (e.g. K8s
// stackdriver entries also carry pod_name). K8s events are the most
// specific (they require a `reason` label) so they win when present.
func inferProvider(labels map[string]string) provider {
	if labels == nil {
		return providerGeneric
	}
	// K8s events: `reason` is the discriminator. K8s normalize.go
	// guarantees `reason` on every event-derived stream.
	if labels["reason"] != "" {
		return providerK8s
	}
	// CloudWatch: log_group + log_stream are unique to CW normalize.
	if labels["log_stream"] != "" || labels["log_group"] != "" {
		return providerCloudWatch
	}
	// Stackdriver: resource_type is unique to GCP normalize.
	if labels["resource_type"] != "" {
		return providerStackdriver
	}
	// Loki: `app` is the canonical Loki convention.
	if labels["app"] != "" {
		return providerLoki
	}
	return providerGeneric
}

// aliasK8s derives `<object>.<reason>`. Object selection prefers
// pod_name (most specific), then service, then kind. Reason is
// preserved verbatim — `OOMKilled` stays `OOMKilled`.
func aliasK8s(labels map[string]string) string {
	object := firstNonEmpty(labels, "pod_name", "service", "related_name", "kind")
	reason := labels["reason"]
	if object == "" {
		return sanitize(reason)
	}
	if reason == "" {
		return sanitize(object)
	}
	return sanitize(object) + "." + sanitize(reason)
}

// aliasLoki derives `<app>@<instance>`. Instance is optional — when
// absent the alias is just the app. Job is intentionally dropped: in
// practice `app` and `job` are redundant and `app` is what humans use.
func aliasLoki(labels map[string]string) string {
	app := labels["app"]
	instance := firstNonEmpty(labels, "instance", "host", "pod", "pod_name")
	if app == "" {
		return sanitize(instance)
	}
	if instance == "" {
		return sanitize(app)
	}
	return sanitize(app) + "@" + sanitize(instance)
}

// aliasStackdriver derives `<host>.<resource_type>` where host prefers
// explicit `host` (set by populateResourceLabels), then pod_name, then
// service. Severity is NOT used — Stackdriver streams group by
// resource, not severity, so resource_type is the clearer suffix.
func aliasStackdriver(labels map[string]string) string {
	host := firstNonEmpty(labels, "host", "pod_name", "instance_id", "service")
	rt := labels["resource_type"]
	if host == "" {
		return sanitize(rt)
	}
	if rt == "" {
		return sanitize(host)
	}
	return sanitize(host) + "." + sanitize(rt)
}

// aliasCloudWatch derives `<log_stream>.<service>` where service falls
// back to the log_group tail. CloudWatch streams are typically per-task
// so log_stream is the right primary identifier.
func aliasCloudWatch(labels map[string]string) string {
	stream := labels["log_stream"]
	svc := firstNonEmpty(labels, "service", "log_group")
	if stream == "" {
		return sanitize(svc)
	}
	if svc == "" {
		return sanitize(stream)
	}
	return sanitize(stream) + "." + sanitize(svc)
}

// aliasGeneric derives the alias from the first two labels by key
// (sorted alphabetically for determinism). This is the catch-all for
// label shapes the inferrer doesn't recognize.
func aliasGeneric(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k, v := range labels {
		if v == "" {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	parts := make([]string, 0, 2)
	for i := 0; i < len(keys) && i < 2; i++ {
		k := keys[i]
		parts = append(parts, sanitize(k)+"="+sanitize(labels[k]))
	}
	return strings.Join(parts, ".")
}
