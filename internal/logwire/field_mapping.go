// SPDX-License-Identifier: Apache-2.0

package logwire

import "strings"

// Canonical field names used in Query.FieldFilters. Backends translate
// these to provider-native equivalents via providerMappings.
const (
	FieldService   = "service"
	FieldHost      = "host"
	FieldNamespace = "namespace"
	FieldPod       = "pod"
	FieldContainer = "container"
	FieldLevel     = "level"
	FieldTraceID   = "trace_id"
)

// providerMappings maps provider names to their canonical-to-native
// field translations. All 10 logsift providers are included so future
// backends need zero mapping changes.
var providerMappings = map[string]map[string]string{
	"cloudwatch": {
		// CloudWatch has no native structured fields — field filters
		// become text-match patterns in the filter expression.
	},
	"loki": {
		FieldService:   "service_name",
		FieldHost:      "node",
		FieldNamespace: "namespace",
		FieldPod:       "pod",
		FieldContainer: "container",
		FieldLevel:     "level",
	},
	"gcp": {
		FieldService:   "resource.labels.service_name",
		FieldHost:      "resource.labels.instance_id",
		FieldNamespace: "resource.labels.namespace_name",
		FieldPod:       "resource.labels.pod_name",
		FieldContainer: "resource.labels.container_name",
		FieldLevel:     "severity",
	},
	"axiom": {
		FieldService:   "service",
		FieldHost:      "host",
		FieldNamespace: "kubernetes.namespace_name",
		FieldPod:       "kubernetes.pod_name",
		FieldContainer: "kubernetes.container_name",
		FieldLevel:     "level",
		FieldTraceID:   "trace_id",
	},
	"azure_monitor": {
		FieldService:   "service",
		FieldHost:      "Computer",
		FieldNamespace: "PodNamespace",
		FieldPod:       "PodName",
		FieldContainer: "ContainerName",
		FieldLevel:     "LogLevel",
	},
	"elasticsearch": {
		FieldService:   "service.name",
		FieldHost:      "host.name",
		FieldNamespace: "kubernetes.namespace",
		FieldPod:       "kubernetes.pod.name",
		FieldContainer: "kubernetes.container.name",
		FieldLevel:     "log.level",
		FieldTraceID:   "trace.id",
	},
	"sumo_logic": {
		FieldService:   "_sourceCategory",
		FieldHost:      "_sourceHost",
		FieldNamespace: "namespace",
		FieldPod:       "pod",
		FieldContainer: "container",
		FieldLevel:     "level",
	},
	"new_relic": {
		FieldService:   "service.name",
		FieldHost:      "hostname",
		FieldNamespace: "kubernetes.namespace_name",
		FieldPod:       "kubernetes.pod_name",
		FieldContainer: "kubernetes.container_name",
		FieldLevel:     "level",
		FieldTraceID:   "trace.id",
	},
	"splunk": {
		FieldService:   "service",
		FieldHost:      "host",
		FieldNamespace: "namespace",
		FieldPod:       "pod",
		FieldContainer: "container_name",
		FieldLevel:     "level",
	},
	"datadog": {
		FieldService:   "service",
		FieldHost:      "host",
		FieldNamespace: "kube_namespace",
		FieldPod:       "kube_pod_name",
		FieldContainer: "kube_container_name",
		FieldLevel:     "status",
		FieldTraceID:   "trace_id",
	},
	"kubernetes": {
		FieldService:   "service",
		FieldHost:      "host",
		FieldNamespace: "namespace",
		FieldPod:       "pod",
		FieldContainer: "container",
		FieldLevel:     "level",
	},
}

// MapFieldFilters translates canonical field names to provider-native names.
// Unknown canonical names pass through unchanged (for provider-specific fields).
// Both field names and values are sanitized to prevent query injection from
// LLM-generated tool calls. If the provider has no mapping entry, all fields
// pass through with sanitization only.
func MapFieldFilters(provider string, filters map[string]string) map[string]string {
	if len(filters) == 0 {
		return nil
	}
	mapping := providerMappings[provider] // nil is fine — no translations
	result := make(map[string]string, len(filters))
	for k, v := range filters {
		k = SanitizeFieldName(k)
		v = SanitizeQueryValue(v)
		if k == "" {
			continue
		}
		if native, ok := mapping[k]; ok {
			result[native] = v
		} else {
			result[k] = v
		}
	}
	return result
}

// SanitizeFieldName restricts field names to safe characters: alphanumeric,
// dots, underscores, and hyphens. Prevents injection of query operators
// through LLM-generated field names.
func SanitizeFieldName(name string) string {
	var sb strings.Builder
	sb.Grow(len(name))
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// SanitizeSourceName restricts source/index/dataset names to safe characters:
// alphanumeric, dots, underscores, hyphens, slashes, and wildcards.
func SanitizeSourceName(name string) string {
	var sb strings.Builder
	sb.Grow(len(name))
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' || r == '/' || r == '*' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// SanitizeQueryValue strips characters that could serve as query operators or
// control flow in log query languages (APL, KQL, SPL, NRQL, Lucene, Datadog).
// Defense-in-depth: backend-specific escape functions provide primary escaping;
// this strips universally dangerous patterns from LLM-generated values.
//
// Allowed: alphanumeric, spaces, dots, hyphens, underscores, colons, slashes,
// at-signs, plus, equals, commas. Stripped: pipes, semicolons, backticks,
// parentheses, brackets, braces, quotes, backslashes, newlines, dollar signs.
func SanitizeQueryValue(value string) string {
	var sb strings.Builder
	sb.Grow(len(value))
	for _, r := range value {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			sb.WriteRune(r)
		case r == ' ' || r == '.' || r == '-' || r == '_' || r == ':' ||
			r == '/' || r == '@' || r == '+' || r == '=' || r == ',':
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
