// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// versionRefusalBody is the JSON the gateway sends with a version refusal.
// Extra fields are ignored, deliberately: the body's shape belongs to the
// gateway's repo and may grow.
type versionRefusalBody struct {
	Minimum        string `json:"minimum"`
	ClientVersion  string `json:"client_version"`
	Platform       string `json:"platform"`
	UpgradeCommand string `json:"upgrade_command"`
	Reason         string `json:"reason"`
}

// VersionRefusalError is the local error a refused cloud request fails with.
//
// The refusal is NEVER swallowed and there is deliberately no degraded path:
// no retry without the version header (the header is what the gateway keys its
// decision on) and no downgrade of the refusal into a warning. The error text
// carries the remedy because it is what the user actually reads.
type VersionRefusalError struct {
	Refusal clientver.Refusal
	Path    string
	Status  int
}

// Error names the minimum, this client's own version and the upgrade command,
// which are the three facts a refused user needs. A refusal whose body this
// client could not read says so, and says WHY and on which transport, rather
// than reporting empty fields as if they were answers.
func (e *VersionRefusalError) Error() string {
	r := e.Refusal
	remedy := r.UpgradeCommand
	if remedy == "" {
		remedy = "knowledge install"
	}
	if r.Minimum == "" {
		diag := r.Diagnostic
		if diag == "" {
			diag = "no diagnostic was recorded"
		}
		return fmt.Sprintf(
			"auth: %s: HTTP %d: the Fulminate gateway refused this client over its version, but this client could not read the refusal (reason %s), so the required minimum is unknown — %s; this client reports version %s on %s; run `%s` to upgrade",
			e.Path, e.Status, r.Reason, diag, r.ClientVersion, r.Platform, remedy)
	}
	return fmt.Sprintf(
		"auth: %s: HTTP %d: the Fulminate gateway refused this client over its version (reason %s): minimum %s, this client %s on %s; run `%s` to upgrade",
		e.Path, e.Status, r.Reason, r.Minimum, r.ClientVersion, r.Platform, remedy)
}

// RefusalObservation is ONE TRANSPORT'S VIEW of a response that may be a gateway
// version refusal: the status, the response headers, however much of the body
// the caller read, the error that read returned, and who is asking.
//
// IT IS A STRUCT BECAUSE THE BODY BYTES ALONE ARE NOT ENOUGH, which is the
// defect this shape closes. Reading a refusal correctly needs the HEADERS as
// well — a body arrives under whatever Content-Encoding the transport asked for
// — and reporting an unreadable one usefully needs the transport's name and the
// read error. Every field a classifier needs travels in one value, so a new
// transport cannot half-supply the inputs and a new input cannot be added by
// changing five call sites.
//
// Header may be nil; http.Header.Get is nil-safe and an absent Content-Encoding
// means the bytes are already the bytes.
type RefusalObservation struct {
	// Status is the HTTP status the transport saw.
	Status int
	// Header is the response header set. Content-Encoding is read from it.
	Header http.Header
	// Body is what the transport read, AS IT ARRIVED — still encoded.
	Body []byte
	// ReadErr is the error the body read returned, if any: a read that FAILED
	// mid-stream, an unexpected EOF, a closed body. Supplying it is what lets
	// such a failure be reported as one rather than as a body that was not JSON.
	//
	// IT DOES NOT COVER TRUNCATION AT THE SIZE CAP, and saying otherwise sends a
	// reader looking for the wrong thing. Every call site reads through
	// io.LimitReader(resp.Body, MaxErrorBodyBytes), and io.ReadAll over a limit
	// reader that hits its limit returns the bytes it got with a NIL error —
	// truncation there is not an error condition. A body cut at the cap
	// therefore takes the ordinary parse path and reports "it is not JSON"; the
	// quoted excerpt in the diagnostic is what shows the reader the cut-off
	// prefix, which is the same evidence by another route.
	ReadErr error
	// Transport names who observed this, for the diagnostic: "connect", "sync",
	// "tunnel". A refusal that cannot say which transport lost the body sends
	// the reader looking in the wrong place.
	Transport string
	// Path is the route, for the error text.
	Path string
}

// LatchVersionRefusal recognizes a gateway version refusal on a non-2xx cloud
// response, LATCHES it process-wide via clientver, and reports what it latched.
//
// ok is false when the response is not a version refusal at all, in which case
// nothing is latched.
//
// It lives in one place because the client has several cloud transports and all
// of them must latch identically — the gateway fronts all client cloud traffic
// and refuses BEFORE FORWARDING, so the refusal can arrive on any of them, on
// any request, at any time. THAT IS ALSO WHY THE CONTENT-ENCODING IS UNDONE HERE
// rather than at a call site: a transport that asks for compression and cannot
// read a compressed refusal is broken in exactly one way, and fixing it once
// here fixes it for every transport, including ones added later.
//
// A 426 whose body this client cannot read is still a refusal: it latches with
// reason [clientver.ReasonRefusalBodyUnparseable] and carries a Diagnostic
// naming the transport, the encoding, the cause and a bounded excerpt. The
// alternative — treating an unreadable refusal as a generic transport error —
// would lose the one signal telling the user their client is blocked.
//
// THAT LOCAL REASON IS DELIBERATELY NOT THE GATEWAY'S version_unparseable, and
// the two were one string until the overload cost a lane a source walk: the
// gateway's spelling means IT could not parse the version this client CLAIMED,
// and reading it as "the client could not parse the body" (or the reverse) sends
// the reader to the wrong repo. See the clientver vocabulary.
//
// An UNRECOGNIZED gateway reason is latched verbatim and never coerced to a
// known member; see the reason vocabulary in clientver for why that is
// load-bearing rather than defensive.
func LatchVersionRefusal(obs RefusalObservation) (*VersionRefusalError, bool) {
	if obs.Status != http.StatusUpgradeRequired {
		return nil, false
	}

	r := clientver.Refusal{
		ClientVersion: clientver.Version,
		Platform:      clientver.Platform(),
	}
	if cause := obs.readGatewayFields(&r); cause != "" {
		// Unreadable, or readable but carrying no reason — neither is an answer
		// this client can explain, and both are still refusals.
		r.Reason = clientver.ReasonRefusalBodyUnparseable
		r.Diagnostic = obs.diagnose(cause)
	}

	clientver.LatchRefusal(r)
	return &VersionRefusalError{Refusal: r, Path: obs.Path, Status: obs.Status}, true
}

// readGatewayFields fills r from the gateway's JSON body and returns "" on
// success, or the CAUSE the body could not be read on failure. The cause is a
// sentence fragment the diagnostic completes, never a full message, so every
// failure renders the same way.
func (o RefusalObservation) readGatewayFields(r *clientver.Refusal) string {
	if o.ReadErr != nil {
		return "reading it failed: " + o.ReadErr.Error()
	}
	body, err := decodeRefusalBody(o.Header, o.Body)
	if err != nil {
		return err.Error()
	}
	var parsed versionRefusalBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		// THE PARSER'S OWN SENTENCE IS DELIBERATELY NOT QUOTED, and that is a
		// standing contract rather than a style choice: a truncated refusal
		// surfacing as "unexpected end of JSON input" reads as a parse failure
		// instead of as a refusal, which is how a blocked user stops seeing the
		// remedy. The tunnel-proxy suite pins it, on a body gorilla's handshake
		// slurp genuinely truncates. The quoted excerpt below carries the same
		// information in a form that cannot be mistaken for the headline — the
		// reader sees the cut-off prefix, or the gzip magic, for themselves.
		return "it is not JSON"
	}
	if parsed.Reason == "" {
		return "it parsed as JSON but named no reason"
	}

	r.Minimum = parsed.Minimum
	r.UpgradeCommand = parsed.UpgradeCommand
	r.Reason = parsed.Reason
	// The gateway echoes back what it saw. Prefer its values when present so a
	// refusal reports the identity the gateway actually judged, and fall back to
	// this client's own only when the body omits them.
	if parsed.ClientVersion != "" {
		r.ClientVersion = parsed.ClientVersion
	}
	if parsed.Platform != "" {
		r.Platform = parsed.Platform
	}
	return ""
}

// decodeRefusalBody undoes whatever Content-Encoding the response carries.
//
// THIS IS THE FIX FOR A REFUSAL THAT ARRIVED COMPRESSED AND WAS REPORTED AS
// UNREADABLE. net/http decompresses transparently ONLY when it added
// Accept-Encoding itself; a client that sets that header takes the bytes as they
// came. The connect protocol sets `Accept-Encoding: gzip` on every unary call
// (protocol_connect.go in connect-go), so every connect response is eligible to
// arrive gzipped, and the gateway's instructive refusal — the one carrying the
// minimum and the upgrade command — was being parsed as gzip bytes and reported
// as an unreadable body with no remedy in it.
//
// AN ENCODING THIS CLIENT CANNOT UNDO IS AN ERROR, never a silent pass-through
// of the encoded bytes: handing those to the JSON parser produces exactly the
// misdiagnosis this function exists to end, one encoding later.
//
// The output is bounded by the same cap as the input. A compressed refusal
// expands, and an unbounded read here would let a small body claim as much
// memory as it liked.
func decodeRefusalBody(h http.Header, body []byte) ([]byte, error) {
	switch enc := strings.ToLower(strings.TrimSpace(h.Get("Content-Encoding"))); enc {
	case "", "identity":
		return body, nil
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("it is declared gzip and does not decompress: %w", err)
		}
		defer func() { _ = zr.Close() }()
		out, rerr := io.ReadAll(io.LimitReader(zr, MaxErrorBodyBytes))
		if rerr != nil {
			return nil, fmt.Errorf("it is declared gzip and does not decompress: %w", rerr)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("it is declared %s, an encoding this client cannot undo", strconv.Quote(enc))
	}
}

// diagnose renders the one line an operator needs when a refusal body could not
// be read: which transport lost it, what encoding it claimed, how many bytes
// arrived, why the read failed, and what the bytes actually begin with.
//
// THE EXCERPT IS QUOTED AND BOUNDED. A compressed or binary body pasted raw into
// an error line is illegible and can carry control characters into a terminal or
// a log; strconv.Quote makes it legible and inert, and the bound keeps a
// multi-kilobyte HTML error page from becoming the error message.
func (o RefusalObservation) diagnose(cause string) string {
	transport := o.Transport
	if transport == "" {
		transport = "(unnamed)"
	}
	enc := o.Header.Get("Content-Encoding")
	if enc == "" {
		enc = "none"
	}
	return fmt.Sprintf(
		"on the %s transport the body arrived under content-encoding %s, %d bytes, and %s; it begins %s",
		transport, enc, len(o.Body), cause, bodyExcerpt(o.Body))
}

// refusalExcerptBytes bounds how much of an unreadable body reaches an error
// line. Long enough to recognize JSON, HTML or a gzip magic number; short
// enough that the remedy stays visible after it.
const refusalExcerptBytes = 160

// bodyExcerpt renders at most refusalExcerptBytes of body, quoted.
func bodyExcerpt(body []byte) string {
	if len(body) == 0 {
		return "(empty)"
	}
	if len(body) > refusalExcerptBytes {
		return strconv.Quote(string(body[:refusalExcerptBytes])) + " (truncated)"
	}
	return strconv.Quote(string(body))
}
