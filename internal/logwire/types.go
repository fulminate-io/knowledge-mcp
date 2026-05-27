// SPDX-License-Identifier: Apache-2.0

// Package logwire holds the slim wire-types crossed between the
// client-side logs/<provider>/ collectors and the server-side ingest
// handler. It carries only data types and the Provider interface — no
// runtime logic. Pipeline, QueryEngine, Materialize, label/aggregation
// machinery stay in their respective owning binaries.
//
// Parallel to domains/collectorwire (Phase 1): a leaf package both
// binaries can import without dragging in the other half of the
// collector tree.
package logwire

import (
	"context"
	"time"
)

// LogEntry is a single log line with its metadata.
type LogEntry struct {
	// Timestamp is when the log line was emitted.
	Timestamp time.Time

	// Severity is the log level (e.g., "INFO", "ERROR").
	Severity string

	// Message is the raw log message text.
	Message string

	// Labels are arbitrary key-value metadata (e.g., namespace, pod, service).
	Labels map[string]string
}

// LogTemplate is a Drain-clustered log pattern. Multiple log entries that
// share the same structure map to a single template, with variable parts
// replaced by <*> wildcards.
type LogTemplate struct {
	// ID is the sha256 hash of the Pattern string.
	ID string

	// Pattern is the Drain template with <*> wildcards replacing variable tokens.
	Pattern string

	// Severity is the log level for this template cluster (first-class field).
	Severity string

	// Count is the number of log entries that matched this template.
	Count int

	// FirstSeen is the timestamp of the earliest matching entry.
	FirstSeen time.Time

	// LastSeen is the timestamp of the most recent matching entry.
	LastSeen time.Time

	// ExampleVars holds variable values from recent matches, capped at a few
	// examples. Each inner slice corresponds to the <*> slots in Pattern.
	ExampleVars [][]string

	// Alias is a human-readable identifier derived from Pattern + Severity
	// (see TemplateAliasFor). May be empty on legacy graphs created before
	// the alias feature was introduced; consumers must recompute via
	// TemplateAliasFor when an empty value is observed.
	Alias string
}

// LogStream is a unique combination of labels identifying a log source.
// Streams are fingerprinted by their low-cardinality labels to enable
// shared label nodes in the graph.
type LogStream struct {
	// ID is a fingerprint hash of the full label set.
	ID string

	// Labels is the complete label set for this stream.
	Labels map[string]string

	// LowCardLabels are labels shared across many streams, stored as
	// graph nodes for cross-stream querying.
	LowCardLabels map[string]string

	// HighCardLabels are labels unique to this stream (e.g., pod name),
	// stored inline rather than as shared nodes.
	HighCardLabels map[string]string

	// Fingerprint is the hex hash of sorted low-cardinality labels.
	Fingerprint string

	// Alias is a human-readable identifier derived from the label set
	// (see AliasFor). May be empty on legacy graphs created before the
	// alias feature was introduced; consumers must recompute via AliasFor
	// when an empty value is observed.
	Alias string
}

// LogChunk is a time-bounded block of compressed log entries belonging
// to a single stream and template. Chunks are the unit of storage in
// the log graph.
type LogChunk struct {
	// ID uniquely identifies this chunk.
	ID string

	// StreamID links to the owning LogStream.
	StreamID string

	// TemplateID links to the LogTemplate these entries matched.
	TemplateID string

	// StartTime is the timestamp of the earliest entry in the chunk.
	StartTime time.Time

	// EndTime is the timestamp of the latest entry in the chunk.
	EndTime time.Time

	// CompressedData holds ZSTD-compressed timestamps and variable values.
	CompressedData []byte

	// EntryCount is the number of log entries in this chunk.
	EntryCount int
}

// Query describes a log retrieval request sent to a Provider.
type Query struct {
	// Provider is the backend to query (e.g., "cloudwatch", "loki").
	Provider string

	// Source is the log source identifier (e.g., log group, stream selector).
	Source string

	// StartTime is the beginning of the query time range.
	StartTime time.Time

	// EndTime is the end of the query time range.
	EndTime time.Time

	// TextFilter is a free-text substring or pattern to match in messages.
	TextFilter string

	// FieldFilters are exact-match filters on log entry labels.
	FieldFilters map[string]string

	// SeverityMin is the minimum severity level to include.
	SeverityMin string

	// MaxEntries caps the number of entries returned.
	MaxEntries int

	// RawQuery is a provider-native query string (e.g., CloudWatch Insights
	// syntax, LogQL). When set, it takes precedence over the structured fields.
	RawQuery string
}

// Source describes a discoverable log source from a Provider.
type Source struct {
	// Name is the source identifier (e.g., "/aws/lambda/my-func").
	Name string

	// Provider is which backend owns this source.
	Provider string

	// Description is a human-readable label for the source.
	Description string
}

// Provider abstracts a log backend (CloudWatch, Loki, local files, etc.).
// Implementations are registered and configured at runtime; the interface
// is defined here to establish type compatibility with the backend adapters.
type Provider interface {
	// Configure applies provider-specific settings.
	Configure(config map[string]string) error

	// Collect streams log entries matching the query. The emit callback
	// receives batches of entries; returning a non-nil error from emit
	// stops collection.
	Collect(ctx context.Context, query Query, emit func(batch []LogEntry) error) error

	// ListSources discovers available log sources matching the prefix.
	ListSources(ctx context.Context, prefix string) ([]Source, error)
}

// CorrelationResult describes a candidate pair of error templates whose
// time ranges overlap. "StructurallyConfirmed" is set when the cloud
// graph dependency checker reports a path between the two owning
// resources; only confirmed pairs become CORRELATES_WITH edges.
// Unconfirmed pairs still surface in the pipeline summary as "possibly
// related" so the LLM can flag them for the user without treating them
// as facts.
type CorrelationResult struct {
	TemplateA             string
	TemplateB             string
	ServiceA              string
	ServiceB              string
	ResourceA             string
	ResourceB             string
	CooccurrenceScore     float64
	StructurallyConfirmed bool
}

// ResolvedProxyEntry is the serializable form of a cloud-resource
// resolution produced by the client-side CloudResolver and consumed by
// the server-side log-graph materializer. LabelKey + LabelValue identify
// the source log-label; Account + ResourceID identify the target cloud
// graph node the client turns into a proxy via
// crossgraph.BuildCrossGraphProxy before emitting it over the wire.
type ResolvedProxyEntry struct {
	LabelKey   string
	LabelValue string
	Account    string
	ResourceID string
}
