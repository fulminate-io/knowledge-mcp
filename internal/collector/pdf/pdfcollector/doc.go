// SPDX-License-Identifier: Apache-2.0

// Package pdfcollector turns a single PDF file into a typed raw-graph
// of document / section / paragraph / code_block / list_item / table /
// block nodes connected by contains edges.
//
// The collector opens the file via the public collector/pdf entry point,
// runs the chunker with chunk.DefaultOptions (ModeSection), and emits
// each chunk as a deterministically-IDed *knowledgev1.Node into a per-source
// graph keyed by a slug derived from the file basename + content hash.
// Every node carries Metadata["source"] = "pdf" so downstream recipes
// stay source-agnostic.
//
// The graph type is kgtypes.GraphPDFRaw, which is excluded from every
// LLM-backed pipeline (summarization, embedding, dream phases) via
// SkipsLLMProcessing. Only a downstream stage-2 translator (recipe
// transformer) is expected to consume the raw graph.
//
// Inputs are restricted to absolute filesystem paths; URL fetching is
// deferred to a future ticket. Encrypted PDFs surface ErrEncrypted from
// the underlying parser unwrapped (errors.Is matches) so callers can
// distinguish password-protected documents from other parse failures.
package pdfcollector
