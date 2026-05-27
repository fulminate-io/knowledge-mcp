// SPDX-License-Identifier: Apache-2.0

package kgtypes

const (
	GraphKnowledge GraphType = "knowledge"
	GraphCode      GraphType = "code"
	GraphCloud     GraphType = "cloud"
	GraphPractice  GraphType = "practice"
	GraphLinkage   GraphType = "linkage"
	GraphCICD      GraphType = "cicd"
	// GraphLogs is an ephemeral per-query graph built from structured log
	// entries. A single query can produce millions of nodes (templates,
	// streams, chunks, labels). Log graphs are MUST NEVER be sent to the
	// summarizer or embedder — doing so would cost thousands of dollars
	// and take hours. SkipsLLMProcessing enforces this at the store layer;
	// dream phases additionally exclude GraphLogs by only iterating
	// specific graph types. Storage: ~/.knowledge/logs/
	GraphLogs GraphType = "logs"
	// GraphTransformers is the first-class graph that stores Transformer
	// recipes — human/agent-authored executable bodies in the graph-agnostic
	// recipe DSL. Each node's Content carries the recipe source; metadata
	// carries source_graph_type, target_graph_type, and target_name. Names
	// are recipe identifiers (e.g. "eip-to-design-patterns"). Storage:
	// ~/.knowledge/transformers/<name>.bin.
	//
	// SkipsLLMProcessing(GraphTransformers) is false — recipe bodies are
	// authored content that benefits from BM25 indexing so operators can
	// discover recipes via query(graph:"transformers", text:"...").
	GraphTransformers GraphType = "transformers"
	// GraphWebRaw is a per-source graph of typed raw-graph records emitted
	// by the web collector (page / section / paragraph / code_block /
	// list / list_item / table / link / image / blockquote nodes with
	// contains/references edges). The content is raw HTML-derived text
	// that a downstream stage-2 translator consumes to synthesize higher-
	// level knowledge nodes — the raw graph itself must NEVER hit the
	// summarizer or embedder. SkipsLLMProcessing enforces this the same
	// way GraphLogs is enforced. Storage: ~/.knowledge/web/<source>.bin
	// where <source> is a slug identifying the crawl (e.g. "hohpe-eip",
	// "go101-go-details-and-tips").
	GraphWebRaw GraphType = "web"
	// GraphPDFRaw is a per-source graph of typed raw-graph records emitted
	// by the PDF collector (document / section / paragraph / code_block /
	// list_item / table / block nodes with contains edges). The content is
	// raw text extracted from a PDF that a downstream stage-2 translator
	// consumes to synthesize higher-level knowledge nodes — the raw graph
	// itself must NEVER hit the summarizer or embedder. SkipsLLMProcessing
	// enforces this the same way GraphWebRaw and GraphLogs are enforced.
	// Storage: ~/.knowledge/pdf/<source>.bin where <source> is a slug
	// derived from the input file basename + content hash so re-collecting
	// the same document is idempotent.
	GraphPDFRaw GraphType = "pdf"
)

// Status values for work nodes. Internal vocabulary, NOT a closed enum —
// see mutateRequestArgs.Status comment for the open-string contract.
const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusSkipped   = "skipped"
)

// Status values for project nodes.
const (
	StatusArchived = "archived"
)

// Status values for ticket nodes.
const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusClosed     = "closed"
)

// Status values for thought nodes.
const (
	StatusHypothesized = "hypothesized"
	StatusValidated    = "validated"
)
