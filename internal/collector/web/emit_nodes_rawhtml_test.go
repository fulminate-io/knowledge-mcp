// SPDX-License-Identifier: Apache-2.0

package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// This file fences the raw_html retention node. It is separate from
// emit_nodes_test.go so neither file approaches the repo's 500-line hard cap.

// rawHTMLPage builds a pageRecord whose retained body is exactly body, with
// ContentHash derived from the SAME bytes — the two fields a round-trip check
// compares, produced here the way parsePage produces them.
func rawHTMLPage(pageURL, body string) *pageRecord {
	return &pageRecord{
		URL:           pageURL,
		FinalURL:      pageURL,
		Title:         "Retention fixture",
		HTTPStatus:    200,
		ContentHash:   hashBody([]byte(body)),
		RawHTMLBase64: base64.StdEncoding.EncodeToString([]byte(body)),
		TopSections: []*sectionRecord{{
			Heading:  "Only Section",
			Depth:    1,
			Children: []contentRecord{paragraphRecord{Text: "some prose"}},
		}},
	}
}

func onlyRawHTMLNode(t *testing.T, nodes []*knowledgev1.Node) *knowledgev1.Node {
	t.Helper()
	var found *knowledgev1.Node
	count := 0
	for _, n := range nodes {
		if n.Type == "raw_html" {
			count++
			found = n
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one raw_html node per page, got %d", count)
	}
	return found
}

// TestEmitFromPage_RawHTMLRoundTripsToContentHash proves the retained bytes
// are the page's actual bytes rather than something merely present.
//
// The round trip is checkable BECAUSE ContentHash and RawHTMLBase64 come from
// one stamper over one buffer: parsePage derives both from the same p.Body in
// the same call, and rawHTMLPage above mirrors that. Hashing the decoded
// base64 and comparing to content_hash therefore compares the retained bytes
// against an independently-computed digest of the same source, not a value
// against itself.
func TestEmitFromPage_RawHTMLRoundTripsToContentHash(t *testing.T) {
	const body = `<html><body><h1>Only Section</h1><p>some prose</p><!-- a comment the walker drops --></body></html>`
	p := rawHTMLPage("https://example.com/retained", body)

	nodes, edges := mustEmitFromPage(t, p, time.Time{})
	raw := onlyRawHTMLNode(t, nodes)

	// 1. The retained bytes decode back to the served body, byte for byte.
	decoded, err := base64.StdEncoding.DecodeString(raw.Metadata["html_base64"])
	if err != nil {
		t.Fatalf("html_base64 does not decode: %v", err)
	}
	if string(decoded) != body {
		t.Errorf("decoded html_base64 != served body\n got: %q\nwant: %q", decoded, body)
	}

	// 2. And they hash to the content_hash carried on the raw_html node AND
	//    on the page node — the same value, reached two ways.
	sum := sha256.Sum256(decoded)
	gotHash := hex.EncodeToString(sum[:])
	if raw.Metadata["content_hash"] != gotHash {
		t.Errorf("raw_html content_hash = %q, decoded bytes hash to %q", raw.Metadata["content_hash"], gotHash)
	}
	var page *knowledgev1.Node
	for _, n := range nodes {
		if n.Type == "page" {
			page = n
		}
	}
	if page == nil {
		t.Fatal("no page node emitted; the cross-check below would be vacuous")
	}
	if page.Metadata["content_hash"] != gotHash {
		t.Errorf("page content_hash = %q, retained bytes hash to %q", page.Metadata["content_hash"], gotHash)
	}

	// 3. html_bytes is the DECODED length, not the encoded one. The guard
	//    below makes that distinction measurable: base64 expands, so an
	//    implementation reporting len(html_base64) yields a different number.
	if len(raw.Metadata["html_base64"]) == len(body) {
		t.Fatal("fixture body encodes to the same length it decodes to; the html_bytes check cannot discriminate")
	}
	if got := raw.Metadata["html_bytes"]; got != strconv.Itoa(len(body)) {
		t.Errorf("html_bytes = %q, want the decoded length %d", got, len(body))
	}

	// 4. THE BINDING CONSTRAINT: the node contributes nothing to the BM25
	//    token index. Those five scalars are exactly the fields composed into
	//    it, so all five empty means the retained markup is invisible to
	//    search. A SymbolName added "for readability" fails right here.
	if raw.SymbolName != "" || raw.Summary != "" || raw.Keywords != "" || raw.Description != "" || raw.Content != "" {
		t.Errorf("raw_html node carries BM25-visible text: symbol=%q summary=%q keywords=%q description=%q content=%q",
			raw.SymbolName, raw.Summary, raw.Keywords, raw.Description, raw.Content)
	}

	// 5. It is addressed and grouped: uri like every other node, FilePath so
	//    the upload chunker keeps it in a frame with its own page.
	if raw.Metadata["uri"] != p.FinalURL {
		t.Errorf("raw_html uri = %q, want %q", raw.Metadata["uri"], p.FinalURL)
	}
	if raw.FilePath != p.URL {
		t.Errorf("raw_html FilePath = %q, want the page URL %q", raw.FilePath, p.URL)
	}

	// 6. It is contained by its page, at the position AFTER the last section
	//    so no existing section position shifted.
	found := false
	for _, e := range edges {
		if e.Type != kgtypes.EdgeContains || e.FromID != page.Id || e.ToID != raw.Id {
			continue
		}
		found = true
		if pos := parseMeta(t, e.Evidence)["position"]; pos != strconv.Itoa(len(p.TopSections)) {
			t.Errorf("raw_html contains position = %q, want %d (after the last section)", pos, len(p.TopSections))
		}
	}
	if !found {
		t.Fatalf("no contains edge from page %s to raw_html %s", page.Id, raw.Id)
	}
}

// TestEmitFromPage_RawHTMLNodesGroupIntoBoundedChunks is a CAPABILITY gate,
// not a spelling check on FilePath: it asserts that a crawl's retained bodies
// can be uploaded in BOUNDED frames rather than one unbounded blob.
//
// Two pages with distinct URLs and large retained bodies are handed to the
// real chunker at a small bound. The target arm requires the two raw_html
// nodes to land in DIFFERENT chunks. The known-negative arm blanks every
// FilePath and requires exactly ONE chunk however small the bound — that is
// the pre-change behaviour, and it is what makes the target arm's split
// attributable to the grouping key rather than to the chunker doing something
// it would have done anyway.
func TestEmitFromPage_RawHTMLNodesGroupIntoBoundedChunks(t *testing.T) {
	bigA := "<html><body>" + strings.Repeat("aaaaaaaa", 4096) + "</body></html>"
	bigB := "<html><body>" + strings.Repeat("bbbbbbbb", 4096) + "</body></html>"

	nodesA, _ := mustEmitFromPage(t, rawHTMLPage("https://example.com/page-a", bigA), time.Time{})
	nodesB, _ := mustEmitFromPage(t, rawHTMLPage("https://example.com/page-b", bigB), time.Time{})
	combined := append(append([]*knowledgev1.Node{}, nodesA...), nodesB...)

	const maxBytes = 8192

	chunks := remote.BatchNodes(combined, maxBytes)
	if len(chunks) <= 1 {
		t.Fatalf("want more than one chunk at a %d-byte bound, got %d — the retained bodies are riding in one frame",
			maxBytes, len(chunks))
	}
	chunkOf := map[string]int{}
	for i, chunk := range chunks {
		for _, n := range chunk {
			if n.Type == "raw_html" {
				chunkOf[n.Id] = i
			}
		}
	}
	if len(chunkOf) != 2 {
		t.Fatalf("want the two pages' raw_html nodes present, found %d", len(chunkOf))
	}
	seen := map[int]bool{}
	for id, idx := range chunkOf {
		if seen[idx] {
			t.Errorf("both raw_html nodes landed in chunk %d (node %s) — the frames are not bounded per page", idx, id)
		}
		seen[idx] = true
	}

	// KNOWN NEGATIVE: without the grouping key the same slice collapses into
	// a single chunk no matter how small the bound.
	blanked := make([]*knowledgev1.Node, 0, len(combined))
	for _, n := range combined {
		clone, ok := proto.Clone(n).(*knowledgev1.Node)
		if !ok {
			t.Fatalf("proto.Clone returned %T, not *knowledgev1.Node", clone)
		}
		clone.FilePath = ""
		blanked = append(blanked, clone)
	}
	if got := len(remote.BatchNodes(blanked, maxBytes)); got != 1 {
		t.Errorf("blanked-FilePath control: want exactly 1 chunk, got %d — the control no longer models the pre-change behaviour", got)
	}
}

// retentionOnlyHTML places a sentinel where the walker structurally cannot
// reach it: inside a <script> body (isNonRenderable skips script/style/
// noscript/template subtrees outright) and inside an HTML comment (not an
// element node at all). Full-HTML retention is the ONLY route by which those
// bytes can enter the graph, which is what makes the absence assertion below
// a statement about retention rather than about extraction.
const (
	retentionSentinel = "zqRETAINEDONLYSENTINELqz"
	retentionOnlyHTML = `<html><head><title>Retention Aperture</title></head><body>
<script>var marker = "` + retentionSentinel + `";</script>
<!-- ` + retentionSentinel + ` in a comment too -->
<article>
<h1>Retention Aperture</h1>
<p>This paragraph is ordinary prose with enough substance that it is emitted as a paragraph node and flattened into the page description.</p>
<p>A second paragraph, so the page node's Description is unambiguously non-empty for the aperture control below.</p>
</article>
</body></html>`
)

// TestEmitFromPage_RawHTMLNeverEntersBM25VisibleFields is the checkable form
// of this phase's binding constraint: the retained HTML must not enter the
// BM25 token index.
//
// SymbolName, Summary, Keywords, Description and Content are the five and only
// five scalars composed into that index (BM25Fields in the server's
// bm25_fields.go, bm25FieldsFromProto in the client pipeline, and indexedFields
// in the bm25 builder all read exactly these). They are asserted by name here
// rather than by calling the composer, because the server is a separate Go
// module and this repo forbids a shared hand-written package between them.
func TestEmitFromPage_RawHTMLNeverEntersBM25VisibleFields(t *testing.T) {
	fetched := fakeFetched("https://example.com/aperture", retentionOnlyHTML)
	p, err := parsePage(fetched, fakeCleaned("Retention Aperture", retentionOnlyHTML))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	nodes, _ := mustEmitFromPage(t, p, time.Time{})

	// KNOWN POSITIVE, same read path: the sentinel IS retained. Without this
	// leg the absence assertion is satisfied by retention doing nothing.
	raw := onlyRawHTMLNode(t, nodes)
	decoded, err := base64.StdEncoding.DecodeString(raw.Metadata["html_base64"])
	if err != nil {
		t.Fatalf("html_base64 does not decode: %v", err)
	}
	if !strings.Contains(string(decoded), retentionSentinel) {
		t.Fatalf("sentinel %q absent from the retained HTML — nothing was retained, so the absence check below proves nothing",
			retentionSentinel)
	}

	// APERTURE CONTROL: at least one node carries non-empty BM25-visible
	// text, so leg 3 is not passing over a set of uniformly empty fields.
	sawText := false
	for _, n := range nodes {
		if n.GetDescription() != "" || n.GetContent() != "" || n.GetSymbolName() != "" {
			sawText = true
			break
		}
	}
	if !sawText {
		t.Fatal("no emitted node carries BM25-visible text at all; the absence assertion below is vacuous")
	}

	// THE ASSERTION, over every emitted node and every indexed field.
	for _, n := range nodes {
		for _, f := range []struct{ name, value string }{
			{"symbol_name", n.GetSymbolName()},
			{"summary", n.GetSummary()},
			{"keywords", n.GetKeywords()},
			{"description", n.GetDescription()},
			{"content", n.GetContent()},
		} {
			if strings.Contains(f.value, retentionSentinel) {
				t.Errorf("node id=%s type=%s: retained-only sentinel reached BM25 field %s (%q)",
					n.Id, n.Type, f.name, f.value)
			}
		}
	}
}

// TestEmitFromPage_NonUTF8BodyStillMarshals is the catcher for anyone who
// later "simplifies" html_base64 into a verbatim html key: proto string fields
// must hold valid UTF-8, so a raw latin-1 page would fail to marshal at the
// first upload rather than at review.
//
// The invalid bytes sit inside a <script> body deliberately. emitFromPage
// returns PRE-sanitization nodes — the client sink sanitizes later — so the
// same bytes in ordinary page text would land in a paragraph node's Content
// and fail the marshal against a CORRECT implementation, making this a test
// about sanitization instead of about retention.
//
// THE KNOWN NEGATIVE BELOW STAYS TRUE and is deliberately left as it is: it
// builds a node directly and never routes it through the sink, so it states
// that the sanitizer can be bypassed only by construction. The matching
// assertion — that the SANITIZED path marshals — lives in package remote, in
// TestSanitizeNodeText_CoversMetadataKeysAndValues, because sanitizeNodeText is
// unexported there and this package cannot call it.
func TestEmitFromPage_NonUTF8BodyStillMarshals(t *testing.T) {
	badBytes := string([]byte{0xFF, 0xFE})
	body := "<html><body><script>var b = \"" + badBytes + "\";</script>" +
		"<article><h1>Latin One</h1><p>Ordinary valid prose in the body of the page.</p></article></body></html>"

	if utf8.ValidString(badBytes) {
		t.Fatal("the fixture bytes are valid UTF-8; this test would prove nothing")
	}

	fetched := fakeFetched("https://example.com/latin1", body)
	p, err := parsePage(fetched, fakeCleaned("Latin One", body))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	nodes, _ := mustEmitFromPage(t, p, time.Time{})
	if len(nodes) == 0 {
		t.Fatal("no nodes emitted; the marshal loop below would be vacuous")
	}

	for _, n := range nodes {
		if _, err := proto.Marshal(n); err != nil {
			t.Errorf("node id=%s type=%s failed to marshal: %v", n.Id, n.Type, err)
		}
	}

	// KNOWN NEGATIVE, same marshal path and the SAME bytes: placed raw in a
	// metadata value they DO fail. That is what proves the base64 encoding is
	// what makes the difference, rather than these bytes being harmless.
	unencoded := &knowledgev1.Node{
		Id:       "control",
		Type:     "raw_html",
		Metadata: map[string]string{"html": badBytes},
	}
	if _, err := proto.Marshal(unencoded); err == nil {
		t.Error("control: raw invalid-UTF-8 bytes in a metadata value marshaled cleanly — the probe cannot go red")
	} else if !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("control: marshal failed for an unexpected reason: %v", err)
	}
}
