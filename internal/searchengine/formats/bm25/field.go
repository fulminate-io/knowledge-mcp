// SPDX-License-Identifier: Apache-2.0

package bm25

import "slices"

// posting represents a single term occurrence in a document within a segment.
// docID is the SEGMENT-LOCAL internal document ID (0..docCount-1), NOT a
// corpus-global id. Ported from the server's posting (bm25_core.go:18).
type posting struct {
	docID uint32 // segment-local internal document ID
	tf    uint16 // term frequency in this document
}

// FieldConfig describes a BM25F field with its name, boost weight, and
// length-normalization parameter. Each field gets its own posting lists and
// document-length tracking, but IDF is computed globally across all fields.
// Ported VERBATIM from cmd/knowledge-server/internal/index/bm25 (bm25.go:74).
type FieldConfig struct {
	// Name is the field identifier (e.g. "symbol_name", "summary").
	Name string
	// Boost is the multiplicative weight applied to a field's BM25 contribution.
	Boost float64
	// B is the length-normalization parameter for this field (0 disables
	// normalization, 0.75 is the BM25 standard).
	B float64
}

// defaultFieldConfigs defines the standard BM25F field configuration. Ported
// VERBATIM from the server (field.go:26) so client-built segments score
// identically to the server index:
//   - symbol_name (boost=2, b=0): name partial-match signal, no length norm.
//   - summary     (boost=2, b=0.75): AI summary, standard normalization.
//   - keywords    (boost=2, b=0): extracted keywords, no normalization.
//   - description (boost=1, b=0.75): human description, standard normalization.
//   - content     (boost=1, b=0.75): full body (knowledge only), standard norm.
var defaultFieldConfigs = []FieldConfig{
	{Name: "symbol_name", Boost: 2, B: 0},
	{Name: "summary", Boost: 2, B: 0.75},
	{Name: "keywords", Boost: 2, B: 0},
	{Name: "description", Boost: 1, B: 0.75},
	{Name: "content", Boost: 1, B: 0.75},
}

// DefaultFieldConfigs returns a fresh copy of the standard BM25F field
// configuration. Mutating the returned slice does NOT affect subsequent calls.
func DefaultFieldConfigs() []FieldConfig {
	return slices.Clone(defaultFieldConfigs)
}

// fieldData is one field's sealed, immutable indexed state inside a segment:
// per-term posting lists, per-(segment-local-docID) document length, and the
// field's total token count. Unlike the server's mutable fieldIndex (atomics +
// xsync.Map), this is a plain immutable struct — a sealed segment is never
// mutated, so no concurrency primitives are needed.
type fieldData struct {
	config      FieldConfig
	postings    map[string][]posting // term → postings (segment-local docIDs)
	docLengths  []int                // segment-local docID → token count in this field
	totalTokens int64                // sum of docLengths (this segment, this field)
}

// scoreField computes the BM25 per-field score contribution for a term. Ported
// VERBATIM from the server (field.go:241) — pure math, no state:
//
//	boost * idf * (tf*(k1+1)) / (tf + k1*(1 - b + b*dl/avgdl))
//
// idf is computed corpus-globally (passed in) and avgDocLen is the corpus-global
// per-field average (passed in) so the score matches a single-index baseline.
func (fd *fieldData) scoreField(tf int, docLen int, idfVal, k1, avgDocLen float64) float64 {
	tfFloat := float64(tf)
	var dlRatio float64
	if avgDocLen > 0 {
		dlRatio = float64(docLen) / avgDocLen
	}
	numerator := tfFloat * (k1 + 1)
	denominator := tfFloat + k1*(1-fd.config.B+fd.config.B*dlRatio)
	return fd.config.Boost * idfVal * numerator / denominator
}
