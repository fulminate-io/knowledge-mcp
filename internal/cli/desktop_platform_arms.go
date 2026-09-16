// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"regexp"
)

// platformQueryKeyLimit is the longest query parameter NAME this wall admits,
// in bytes. It is a named declaration rather than a number inside the pattern
// below so a test can compute the boundary from it: written into the pattern
// alone, the admitted side of the bound could only be asserted by a literal,
// and a repeat count off by one would refuse a legitimate name with no test
// moving.
const platformQueryKeyLimit = 64

// platformQueryKey bounds a query parameter NAME. This side has no copy of the
// schema that declares which names a template admits — that closed derived set
// is enforced in the Desktop, over the module generated from the document — so
// what this wall enforces is the shape such a name can take: an identifier, not
// a fragment of a query string. A name carrying '=', '&', '?' or '#' could only
// be an attempt to smuggle a second parameter past an encoder, and an empty
// name is not a parameter at all.
var platformQueryKey = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z][A-Za-z0-9_.-]{0,%d}$`, platformQueryKeyLimit-1))

// platformQueryValueLimit bounds a query parameter VALUE in bytes.
//
// A value is bounded by its LENGTH AND ITS TYPE, never by a character class.
// The website's own filters include an RFC3339 timestamp with colons, an email
// address, free user text and the empty string, so a charset like
// platformSegment's — which a path segment needs, because there the character
// class is what makes a traversal impossible — would refuse input the website
// legitimately sends. What makes a query value safe is that this command
// ENCODES it: the value never reaches the URL unescaped, so no character in it
// can mean anything to the path, the query or the fragment.
const platformQueryValueLimit = 2048

// platformJSONMediaType is the one media type the JSON arm answers for.
const platformJSONMediaType = "application/json"

// platformContentTypeLimit bounds the content type this command repeats back to
// the renderer, which sets it as a response header: a header the platform sent
// is not a header this side should carry at any length.
const platformContentTypeLimit = 256

// encodePlatformQuery validates one request's query parameters and returns the
// encoded query string.
//
// THE ENCODING IS THIS COMMAND'S, never the caller's. The renderer supplies
// pairs and this function produces the string, so a value cannot introduce a
// second parameter, a fragment or a path: url.Values escapes every delimiter it
// carries. Encode also sorts by key, so the same intent produces the same URL.
//
// What it refuses: a key that is not an identifier (the shape a declared query
// parameter can take — the closed per-template key set is enforced in the
// Desktop, over the module generated from the schema this repository does not
// have), a value that is not a JSON string, and a value over the byte bound. A
// JSON null is refused by the same type assertion as a number or an object,
// which is why the field's values are typed `any`.
func encodePlatformQuery(query map[string]any) (string, bool) {
	if len(query) == 0 {
		return "", true
	}
	values := url.Values{}
	for name, supplied := range query {
		if !platformQueryKey.MatchString(name) {
			return "", false
		}
		value, ok := supplied.(string)
		if !ok || len(value) > platformQueryValueLimit {
			return "", false
		}
		values.Set(name, value)
	}
	return values.Encode(), true
}

// platformResponseArm decides which response arm a platform answer takes, from
// its CONTENT TYPE alone.
//
// It is the content type and never the bytes, because "these bytes do not
// parse as JSON, so they must be a download" would turn a gateway's HTML error
// page into a successful file. A JSON-typed body that is not valid JSON stays
// the failure it has always been.
//
// An ABSENT content type is the JSON arm. That is the 204 the platform sends
// when it has nothing to say, whose answer this command has always carried as
// `null`; read as binary instead, a no-content answer would arrive at the
// renderer as an empty download.
//
// A content type that does not parse, or one longer than the bound, is refused
// (ok false): this command repeats the type to the renderer, which makes it a
// response header, and a header it cannot vouch for is not one to pass on.
func platformResponseArm(contentType string) (binary, ok bool) {
	if contentType == "" {
		return false, true
	}
	if len(contentType) > platformContentTypeLimit {
		return false, false
	}
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false, false
	}
	return media != platformJSONMediaType, true
}

// platformBinaryResult builds the binary arm. The base64 is forced by the seam
// rather than chosen: the Desktop reads this command's stdout as one JSON
// document, and a CSV or a PDF is not a JSON string without it.
func platformBinaryResult(status int, contentType string, raw []byte) desktopPlatformResult {
	encoded := base64.StdEncoding.EncodeToString(raw)
	return desktopPlatformResult{Status: status, ContentType: contentType, BodyBase64: &encoded}
}

// platformJSONResult builds the JSON arm, carrying the platform's body
// verbatim. An empty body becomes `null` so a 204 is an answer the renderer can
// parse rather than a parse failure.
func platformJSONResult(status int, raw []byte) (desktopPlatformResult, bool) {
	body := raw
	if len(body) == 0 {
		body = []byte("null")
	}
	if !json.Valid(body) {
		return desktopPlatformResult{}, false
	}
	return desktopPlatformResult{Status: status, Body: body}, true
}
