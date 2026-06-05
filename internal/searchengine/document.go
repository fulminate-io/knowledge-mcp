package searchengine

// ExternalID is the caller-facing identity of a document. It is the stable key
// the engine routes deletes by and the key formats echo back in Hit.
type ExternalID = string

// Document is the neutral unit the engine indexes. It is deliberately decoupled
// from any store/node type so the engine carries no graph coupling: the
// client-side adapter (docFromNode) populates Fields + Vector before Add.
//
// Formats read Fields DEFENSIVELY: a missing key is treated as "" / skipped,
// never a panic. BM25 indexes the text Fields; HNSW ignores Fields and uses
// Vector. Adding a field means adding a constant below, populating it in the
// adapter, and having a consuming format opt in.
type Document struct {
	ID     ExternalID
	Fields map[string]string
	Vector []byte
}

// Hit is one search result. Score is higher-is-better; an HNSW format maps its
// distance to a similarity so the engine can merge top-k across formats uniformly.
type Hit struct {
	ID    ExternalID
	Score float64
}

// Documented Document.Fields keys. Formats MUST tolerate absent keys.
const (
	FieldSummary     = "summary"
	FieldKeywords    = "keywords"
	FieldDescription = "description"
	FieldContent     = "content"
	FieldSymbolName  = "symbol_name"
)
