// SPDX-License-Identifier: Apache-2.0

package kgtypes

// Shared metadata-key vocabulary. These literal key strings are SHARED WIRE
// VOCABULARY: the client writes them onto node / graph metadata and the server
// reads the SAME literal keys back off that metadata (and vice versa). They
// therefore live in kgtypes — the wire-vocab leaf — alongside the
// NodeType/EdgeType/GraphType const vocabulary.

// LLM-pipeline failure marker keys. The pipeline v2 worker writes these keys
// onto a node's metadata when the LLM call returns a TERMINAL error
// (4xx-other / context-too-large / config error — anything that won't be
// resolved by retrying). Discovery shims subsequently exclude nodes whose
// marker is non-empty so a single bad node never burns infinite worker time.
//
// Operator recovery path: manage(operation="clear_llm_failures") resets these
// markers across one or all loaded graphs and the node re-enters the
// discovery pipeline on the next collector tick.
//
// Transient errors (429 / 5xx / network) DO NOT write a marker — they get
// retried on the next tick by the natural discovery loop.
const (
	// MetaKeySummaryFailureReason is the metadata key that marks a node as
	// having failed terminal-error summarization. Empty value = no failure
	// (re-eligible). Non-empty value = stop attempting; operator must clear
	// via manage(clear_llm_failures).
	MetaKeySummaryFailureReason = "summary_failure_reason"

	// MetaKeyEmbedFailureReason is the metadata key that marks a node as
	// having failed terminal-error embedding. Same operator-clear contract
	// as MetaKeySummaryFailureReason.
	MetaKeyEmbedFailureReason = "embed_failure_reason"
)

// Code-graph metadata keys read once by the client-side collector and shipped
// over the wire (FUL-241 Phase 5): the server is filesystem-blind for
// code-graph paths, so topology analyzers read these off graph metadata
// instead of opening go.mod / .knowledge/topology_layers.yaml on the pod.
const (
	// ModulePathKey stores the Go module path for a code graph. Read once by
	// the client-side collector from `<rootDir>/go.mod`; pkg/topology/dsm.go
	// reads it server-side in lieu of opening go.mod.
	ModulePathKey = "kgmeta\x00module_path"

	// LayerConfigKey stores the raw YAML body of
	// `.knowledge/topology_layers.yaml` when the file exists in the repo.
	// Same client-side-read / server-graph-read split as ModulePathKey.
	LayerConfigKey = "kgmeta\x00layer_config"
)
