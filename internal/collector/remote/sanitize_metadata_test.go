// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
)

// These four legs live in package remote, NOT in package web, because
// sanitizeNodeText is unexported here: package web cannot call the sanitizer at
// all, so the "sanitized path marshals" assertion has no other home. The web
// package's own TestEmitFromPage_NonUTF8BodyStillMarshals keeps its
// known-negative control, which remains true and is what shows the sanitizer can
// only be bypassed by constructing a node directly.

// TestSanitizeNodeText_CoversMetadataKeysAndValues observes a node going from
// failing the protobuf marshal to succeeding, because of the metadata map alone.
func TestSanitizeNodeText_CoversMetadataKeysAndValues(t *testing.T) {
	const (
		badKey   = "k\xff\xfe"
		badValue = "v\xff\xfe"
		nulValue = "has\x00nul"
	)
	// Guard the fixture: bytes that were accidentally valid would make the whole
	// test prove nothing.
	require.False(t, utf8.ValidString(badKey), "the bad-key fixture must actually be invalid UTF-8")
	require.False(t, utf8.ValidString(badValue), "the bad-value fixture must actually be invalid UTF-8")

	n := &knowledgev1.Node{
		Id:   "n1",
		Type: "page",
		Metadata: map[string]string{
			badKey:   "value under an invalid key",
			"badval": badValue,
			"nulval": nulValue,
			"clean":  "an ordinary value",
		},
	}

	// KNOWN NEGATIVE ON THE SAME MARSHAL PATH: unsanitized, this node must fail,
	// so the probe is shown able to go red before it is trusted green.
	_, err := proto.Marshal(n)
	require.Error(t, err, "the unsanitized node must fail to marshal, or this test proves nothing")
	require.Contains(t, err.Error(), "UTF-8", "the unsanitized node must fail for the UTF-8 reason: %v", err)

	sanitizeNodeText(n)

	_, err = proto.Marshal(n)
	require.NoError(t, err, "sanitized node still fails to marshal")
	for k, v := range n.Metadata {
		assert.True(t, utf8.ValidString(k), "metadata key not coerced to valid UTF-8: %q", k)
		assert.True(t, utf8.ValidString(v), "metadata value not coerced to valid UTF-8: %q", v)
		assert.NotContains(t, v, "\x00", "metadata value still carries a NUL: %q", v)
	}
	assert.Len(t, n.Metadata, 4, "the four keys sanitize to four distinct spellings, so nothing may be lost: %v", n.Metadata)
}

// TestSanitizeNodeText_MetadataDoesNotMoveTheContributionHash is a
// CHARACTERIZATION GUARD and is labeled as one: it passes against the unfixed
// tree too, because contribhash.NodeContributionHash never had Metadata among
// its inputs — the hash's own doc records Metadata, Summary and Keywords as
// excluded server-added fields.
//
// It is carried deliberately so a future author who adds Metadata to the hash,
// or who bumps ContributionHashSchemeVersion for this change, goes red. THE
// CONSEQUENCE FOR THIS CHANGE: the scheme version stays at 2. The composition
// surface that constant guards does not include Metadata, and a bump would force
// a full re-collect of every graph for no change in what is hashed.
func TestSanitizeNodeText_MetadataDoesNotMoveTheContributionHash(t *testing.T) {
	n := &knowledgev1.Node{
		Id:         "n1",
		Type:       "page",
		SymbolName: "Ordinary",
		Content:    "Content that is not touched by the metadata walk at all.",
		Metadata: map[string]string{
			"k\xff\xfe": "value under an invalid key",
			"badval":    "v\xff\xfe",
			"clean":     "an ordinary value",
		},
	}

	before := contribhash.NodeContributionHash(n)
	sanitizeNodeText(n)
	after := contribhash.NodeContributionHash(n)

	assert.Equal(t, before, after,
		"sanitizing metadata must not move the node contribution hash: no stored hash may shift and no scheme-version bump is owed")
}

// TestSanitizeNodeText_CollidingMetadataKeysResolveDeterministically pins the
// many-to-one collapse: two distinct keys sanitize to ONE spelling, and the
// survivor must be the same on every run.
//
// Go randomizes map iteration order, so a ranged rewrite would pick a different
// survivor per run. Sixty-four runs is what makes that a measurement rather than
// an assertion: a randomized implementation has a vanishing chance of agreeing
// with itself sixty-four times.
func TestSanitizeNodeText_CollidingMetadataKeysResolveDeterministically(t *testing.T) {
	const (
		lesserOriginal  = "dup\xfekey" // 0xfe < 0xff, so this original wins
		greaterOriginal = "dup\xffkey"
		lesserValue     = "from-fe"
		greaterValue    = "from-ff"
	)
	require.NotEqual(t, lesserOriginal, greaterOriginal, "the two originals must be distinct keys")
	require.Equal(t, sanitizeText(lesserOriginal), sanitizeText(greaterOriginal),
		"the two originals must sanitize to ONE spelling, or there is no collision to resolve")

	var winner string
	for run := range 64 {
		n := &knowledgev1.Node{
			Id:   "n1",
			Type: "page",
			Metadata: map[string]string{
				lesserOriginal:  lesserValue,
				greaterOriginal: greaterValue,
			},
		}
		sanitizeNodeText(n)

		require.Len(t, n.Metadata, 1,
			"run %d: expected the colliding pair to collapse to one entry, got %v", run, n.Metadata)
		got := n.Metadata[sanitizeText(lesserOriginal)]
		if run == 0 {
			winner = got
			assert.Equal(t, lesserValue, winner,
				"the lexicographically lesser ORIGINAL key must win the collapse")
			t.Logf("deterministic collision winner across 64 runs: %q", winner)
			continue
		}
		require.Equal(t, winner, got,
			"run %d: the collision survivor changed between runs — the walk is not deterministic", run)
	}
}

// TestSanitizeNodeText_PDFInfoDictMetadataMarshals is the pdf half. A document
// root's title and producer come straight from the PDF Info dict, which pdfcpu's
// accessors pass through unvalidated, so raw bytes from a malformed file reach
// the metadata map exactly as they were written.
func TestSanitizeNodeText_PDFInfoDictMetadataMarshals(t *testing.T) {
	badTitle := "Rapport Financi\xffre"
	badProducer := "Acrobat Distiller \xff.0"
	require.False(t, utf8.ValidString(badTitle), "the title fixture must actually be invalid UTF-8")
	require.False(t, utf8.ValidString(badProducer), "the producer fixture must actually be invalid UTF-8")

	n := &knowledgev1.Node{
		Id:         "pdf:doc",
		Type:       "document",
		SymbolName: badTitle,
		Source:     "pdf-collect",
		Metadata: map[string]string{
			"source":                   "pdf",
			"path":                     "/tmp/report.pdf",
			"collector_schema_version": "1",
			"title":                    badTitle,
			"producer":                 badProducer,
		},
	}

	_, err := proto.Marshal(n)
	require.Error(t, err, "the unsanitized Info-dict node must fail to marshal, or this test proves nothing")

	sanitizeNodeText(n)

	_, err = proto.Marshal(n)
	require.NoError(t, err, "sanitized Info-dict node still fails to marshal")
	assert.True(t, utf8.ValidString(n.Metadata["title"]), "the Info-dict title was not coerced: %q", n.Metadata["title"])
	assert.True(t, utf8.ValidString(n.Metadata["producer"]), "the Info-dict producer was not coerced: %q", n.Metadata["producer"])
	assert.Equal(t, "pdf", n.Metadata["source"], "an already-clean Info-dict entry must survive unchanged")
	assert.NotContains(t, n.Metadata["title"], "\x00", "the coerced title must carry no NUL")
}
