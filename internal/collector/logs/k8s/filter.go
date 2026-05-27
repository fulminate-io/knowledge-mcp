// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// listPageSize bounds the per-page ListOptions.Limit. Chosen to match
// emitBatchSize so a single server round-trip produces at most one emit
// batch. The total-result cap comes from query.MaxEntries, applied to
// normalized LogEntry counts (not Event counts) in drainEvents.
const listPageSize = 500

// buildListOptions constructs metav1.ListOptions for an events.k8s.io/v1
// Events List call from a logwire.Query.
//
// Server-side mappings (reduce data transferred from the apiserver):
//
//   - query.SeverityMin >= WARN → FieldSelector "type=Warning". Events carry
//     two Types (Normal, Warning); anything WARN or above only needs
//     Warning. Lower severities (INFO, DEBUG, TRACE, or empty) fetch all.
//   - query.FieldFilters canonical keys translate to events.k8s.io/v1
//     selector keys:
//     namespace → regarding.namespace
//     kind → regarding.kind
//     reason → reason
//     type → type
//     Unknown keys are left off the server-side selector — they'd likely be
//     rejected by the apiserver's allowed-field list anyway. Client-side
//     filtering in passesClientFilter is the universal safety net.
//
// The caller is expected to graceful-degrade: if the List call returns a
// BadRequest-style error mentioning the field selector (older apiservers,
// custom field-selector allowlists), retry without FieldSelector and rely
// entirely on client-side filtering.
func buildListOptions(query logwire.Query) metav1.ListOptions {
	opts := metav1.ListOptions{Limit: listPageSize}

	var selectors []string
	if wantsWarningsOnly(query.SeverityMin) {
		selectors = append(selectors, "type=Warning")
	}
	selectors = append(selectors, fieldSelectorClauses(query.FieldFilters)...)
	if len(selectors) > 0 {
		opts.FieldSelector = strings.Join(selectors, ",")
	}
	return opts
}

// wantsWarningsOnly reports whether the client-requested minimum severity
// is WARN or higher — in which case we can safely restrict the server-side
// List to type=Warning events. An empty SeverityMin fetches everything;
// INFO/DEBUG/TRACE also fetch everything since Kubernetes Events carry no
// severities below WARN.
func wantsWarningsOnly(severityMin string) bool {
	if severityMin == "" {
		return false
	}
	return logwire.SeverityAtLeast(severityMin, logwire.SeverityWarn)
}

// fieldSelectorClauses translates canonical logwire.Query field filter keys
// into events.k8s.io/v1 field-selector clauses. Keys outside the known
// mapping set are dropped (not passed through): the apiserver rejects
// unknown field selectors with a BadRequest, and silently dropping unknown
// keys here lets the client-side filter in passesClientFilter honor them
// instead.
//
// Canonical → events.k8s.io/v1:
//
//	namespace → regarding.namespace
//	kind      → regarding.kind
//	reason    → reason
//	type      → type
func fieldSelectorClauses(filters map[string]string) []string {
	if len(filters) == 0 {
		return nil
	}
	keys := make([]string, 0, len(filters))
	for k := range filters {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic for test / debug output

	clauses := make([]string, 0, len(filters))
	for _, k := range keys {
		v := logwire.SanitizeQueryValue(filters[k])
		if v == "" {
			continue
		}
		field := canonicalToEventsV1Field(k)
		if field == "" {
			continue
		}
		clauses = append(clauses, field+"="+v)
	}
	return clauses
}

// canonicalToEventsV1Field maps a canonical logwire.Query field key to its
// events.k8s.io/v1 selector path. Returns "" for keys with no server-side
// equivalent (client-side filtering handles those).
func canonicalToEventsV1Field(key string) string {
	switch key {
	case logwire.FieldNamespace:
		return "regarding.namespace"
	case "kind":
		return "regarding.kind"
	case "reason":
		return "reason"
	case "type":
		return "type"
	default:
		return ""
	}
}

// passesClientFilter applies logwire.Query predicates after normalizeEvent
// maps an Event to one or more LogEntry values. Safety-net responsibilities:
//
//   - Severity: a server-side type=Warning selector already narrows the
//     set, but an operator may request ERROR / CRITICAL which cannot be
//     expressed server-side — we confirm the normalized entry's severity
//     meets query.SeverityMin.
//   - Text: Events API has no server-side substring filter.
//   - Time range: Events API has no server-side time filter; we drop
//     entries whose normalized Timestamp lies outside [StartTime, EndTime]
//     when either bound is non-zero (zero means "no bound on that side").
//   - FieldFilters not translatable server-side (e.g. unknown canonical
//     keys) are matched against the entry's Labels map here.
func passesClientFilter(entry logwire.LogEntry, query logwire.Query) bool {
	if !passesTimeRange(entry, query) {
		return false
	}
	if !passesSeverity(entry, query) {
		return false
	}
	if !passesText(entry, query) {
		return false
	}
	return passesFieldFilters(entry, query)
}

func passesTimeRange(entry logwire.LogEntry, query logwire.Query) bool {
	if !query.StartTime.IsZero() && entry.Timestamp.Before(query.StartTime) {
		return false
	}
	if !query.EndTime.IsZero() && entry.Timestamp.After(query.EndTime) {
		return false
	}
	return true
}

func passesSeverity(entry logwire.LogEntry, query logwire.Query) bool {
	if query.SeverityMin == "" {
		return true
	}
	return logwire.SeverityAtLeast(entry.Severity, query.SeverityMin)
}

func passesText(entry logwire.LogEntry, query logwire.Query) bool {
	if query.TextFilter == "" {
		return true
	}
	return strings.Contains(
		strings.ToLower(entry.Message),
		strings.ToLower(query.TextFilter),
	)
}

// passesFieldFilters checks Labels-level equality for any canonical
// FieldFilters entry we couldn't push down server-side. Entries that were
// pushed down are still checked here as a belt-and-suspenders guard against
// the graceful-degrade path silently returning unfiltered results.
func passesFieldFilters(entry logwire.LogEntry, query logwire.Query) bool {
	if len(query.FieldFilters) == 0 {
		return true
	}
	for k, want := range query.FieldFilters {
		want = logwire.SanitizeQueryValue(want)
		if want == "" {
			continue
		}
		labelKey := canonicalToLabelKey(k)
		if entry.Labels[labelKey] != want {
			return false
		}
	}
	return true
}

// canonicalToLabelKey maps a canonical logwire.Query field key to the label
// key emitted by normalizeEvent. Unknown canonical keys fall through
// unchanged so operator-supplied label names still match.
func canonicalToLabelKey(key string) string {
	switch key {
	case logwire.FieldNamespace:
		return "namespace"
	case logwire.FieldService:
		return "service"
	case logwire.FieldPod:
		return "pod_name"
	default:
		return key
	}
}

// isFieldSelectorRejection reports whether an apiserver error likely
// indicates the server refused our field selector. Used by the
// graceful-degrade path in drainEvents: on such a rejection we retry the
// same page with FieldSelector cleared and disable server-side filtering
// for the remainder of the Collect call.
//
// We check the error text for common selector-rejection substrings rather
// than relying on apierrors.IsBadRequest alone — the apiserver returns
// BadRequest for many other reasons (malformed Continue token, invalid
// LabelSelector, etc.), and we only want to degrade for the selector case.
func isFieldSelectorRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "fieldselector") ||
		strings.Contains(msg, "field selector") ||
		strings.Contains(msg, "field label") ||
		strings.Contains(msg, "not a known field")
}
