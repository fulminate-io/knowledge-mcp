// SPDX-License-Identifier: Apache-2.0

// Package logs provides core types and algorithms for log graph construction.
//
// The package implements:
//   - Log entry types (wirelogs.LogEntry, wirelogs.LogTemplate, wirelogs.LogStream, wirelogs.LogChunk) and their
//     graph representations as knowledgev1.Node and knowledgev1.Edge.
//   - A Drain clustering engine that groups raw log messages into parameterized
//     templates via an online prefix-tree algorithm.
//   - Severity parsing and embedded severity detection for reclassifying log
//     lines whose transport-level severity is unreliable (e.g., GKE stderr).
//   - Cardinality tracking for label keys, enabling automatic classification
//     into low-cardinality (shared graph nodes) and high-cardinality (inline).
//   - Stream fingerprinting that produces deterministic identifiers from label
//     sets, with shared label nodes for efficient graph queries.
//   - Consolidators that merge language-specific noise fragments (Go stack
//     traces, Python tracebacks) into coherent templates.
//
// The wirelogs.Provider interface defines the contract for log source backends (e.g.,
// CloudWatch, Loki, GCP Logging). wirelogs.Provider implementations live in separate
// packages; this package provides only the interface and shared types.
package logs
