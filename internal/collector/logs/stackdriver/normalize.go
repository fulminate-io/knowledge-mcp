// SPDX-License-Identifier: Apache-2.0

package stackdriver

import (
	"encoding/json"
	"fmt"
	"strings"

	"cloud.google.com/go/logging"
	"google.golang.org/protobuf/types/known/structpb"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// normalizeEntry converts a GCP *logging.Entry into a logwire.LogEntry. The
// function is organized so callers can see the three normalization stages:
// payload → message, GCP severity → canonical severity (with GKE-aware
// downward reclassification), and resource/entry labels → Labels map.
func normalizeEntry(entry *logging.Entry, projectID string) logwire.LogEntry {
	le := logwire.LogEntry{
		Timestamp: entry.Timestamp,
		Severity:  mapGCPSeverity(entry.Severity),
		Message:   extractMessage(entry),
		Labels:    make(map[string]string, 8),
	}

	// GKE stderr-as-ERROR fix: the container runtime tags all stderr
	// output with ERROR severity even when the actual log line is INFO or
	// DEBUG. Reclassify DOWNWARD — never upgrade — so operators filtering
	// for real errors don't drown in container chatter.
	if embedded := logwire.DetectEmbeddedSeverity(le.Message); embedded != "" {
		if !logwire.SeverityAtLeast(embedded, le.Severity) {
			le.Severity = embedded
		}
	}

	populateResourceLabels(&le, entry)
	populateEntryLabels(&le, entry)

	// Always record the originating project and log name so downstream
	// streams carry the provenance needed for cross-project dashboards.
	le.Labels["project_id"] = projectID
	if entry.LogName != "" {
		le.Labels["log_name"] = extractLogID(entry.LogName)
	}

	return le
}

// populateResourceLabels copies GCP monitored-resource labels into the
// canonical Labels map. We prefer K8s container conventions because that's
// the most common GKE shape; falling back through Cloud Run ("service_name")
// and App Engine ("module_id") keeps the provider useful beyond Kubernetes.
func populateResourceLabels(le *logwire.LogEntry, entry *logging.Entry) {
	if entry.Resource == nil || entry.Resource.Labels == nil {
		return
	}
	labels := entry.Resource.Labels

	// Service selection mirrors logsift/backend/gcp: container_name →
	// service_name → module_id. The first match wins so Cloud Run
	// doesn't overwrite a K8s container label.
	switch {
	case labels["container_name"] != "":
		le.Labels["service"] = labels["container_name"]
	case labels["service_name"] != "":
		le.Labels["service"] = labels["service_name"]
	case labels["module_id"] != "":
		le.Labels["service"] = labels["module_id"]
	}

	// Host selection: instance_id (GCE, Cloud Run) or pod_name (GKE).
	switch {
	case labels["instance_id"] != "":
		le.Labels["host"] = labels["instance_id"]
	case labels["pod_name"] != "":
		le.Labels["host"] = labels["pod_name"]
	}

	// Copy the remaining high-value resource labels so downstream stream
	// grouping can discriminate between pods / clusters / namespaces.
	for _, key := range []string{"namespace_name", "cluster_name", "pod_name", "container_name", "project_id"} {
		if v, ok := labels[key]; ok && v != "" {
			le.Labels[key] = v
		}
	}
	if rt := entry.Resource.Type; rt != "" {
		le.Labels["resource_type"] = rt
	}
}

// populateEntryLabels copies entry-level labels, giving precedence to
// values already set from the monitored resource (resource labels are the
// more authoritative source). The Kubernetes app label is promoted to
// "service" when no resource-level service has been discovered.
func populateEntryLabels(le *logwire.LogEntry, entry *logging.Entry) {
	if entry.Labels == nil {
		return
	}
	for k, v := range entry.Labels {
		if _, taken := le.Labels[k]; taken || v == "" {
			continue
		}
		le.Labels[k] = v
	}
	if _, hasService := le.Labels["service"]; !hasService {
		if app := entry.Labels["k8s-pod/app"]; app != "" {
			le.Labels["service"] = app
		}
	}
}

// extractMessage pulls a human-readable message out of a GCP entry's
// polymorphic payload. The SDK returns:
//   - string                  → textPayload
//   - *structpb.Struct        → jsonPayload (raw proto Struct, not map)
//   - map[string]any          → defensive: some SDK versions return maps
//   - proto.Message or nil    → protoPayload or missing payload
//
// For structured payloads we defer to logwire.ExtractMessageFromMap so the
// canonical "message"/"msg"/"event"/"textPayload" extraction logic is
// shared across providers.
func extractMessage(entry *logging.Entry) string {
	switch p := entry.Payload.(type) {
	case string:
		return p
	case *structpb.Struct:
		if p == nil {
			return ""
		}
		return logwire.ExtractMessageFromMap(p.AsMap())
	case map[string]any:
		return logwire.ExtractMessageFromMap(p)
	default:
		if entry.Payload == nil {
			return ""
		}
		b, err := json.Marshal(entry.Payload)
		if err != nil {
			return fmt.Sprintf("%v", entry.Payload)
		}
		return string(b)
	}
}

// extractLogID strips the "projects/<project>/logs/" prefix from a full GCP
// log name, returning the short log ID. GCP percent-encodes the forward
// slash in log IDs such as "cloudaudit.googleapis.com/activity" — we decode
// that back so downstream labels look like the IDs operators see in the
// Console.
func extractLogID(fullName string) string {
	parts := strings.SplitN(fullName, "/logs/", 2)
	if len(parts) != 2 {
		return fullName
	}
	return strings.ReplaceAll(parts[1], "%2F", "/")
}

// compile-time assertion that the provider satisfies logwire.Provider. Keeping
// this at the end of the file means a signature drift surfaces as a tiny
// compile error instead of a confusing registry panic at runtime.
var _ logwire.Provider = (*stackdriverProvider)(nil)
