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
	// Dtype names the REPRESENTATION Vector is in — see DtypeUbinary /
	// DtypeFloat32. It is what a vector format needs in order to know which
	// metric ranks these bytes, and it cannot be recovered from the bytes
	// themselves: 1024 bytes is 8192 ubinary dimensions or 256 float32 ones, and
	// both are widths this build accepts, so a format inferring the dtype from
	// the length would be guessing between two legal readings.
	//
	// EMPTY MEANS DtypeUbinary, matching the on-disk convention exactly. Every
	// vector segment written before this field existed carries a zero dtype tag,
	// and tag 0 IS ubinary, so an unset Dtype and a historical segment say the
	// same thing. Producers that know the graph's representation SET IT
	// EXPLICITLY rather than relying on the zero value; the empty reading exists
	// for the fixtures and the history that predate the field, not as a default
	// for new code to lean on.
	Dtype string
}

// Vector representations a Document.Vector may be in. They are the same
// spellings the embed config and a graph's recorded embed identity use, so a
// dtype travels from config to segment without being translated on the way.
const (
	DtypeUbinary = "ubinary"
	DtypeFloat32 = "float32"
)

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
