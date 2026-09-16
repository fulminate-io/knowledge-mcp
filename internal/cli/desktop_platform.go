// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// desktopPlatformRequest is the whole request vocabulary, and the whole of it
// is the fields declared below. DisallowUnknownFields is what makes that a refusal rather
// than a convention: a request naming url, host, headers, token or
// authorization is rejected by the decoder before anything reads it, so there
// is no field on this struct through which a caller could supply a credential
// or a destination. The bearer is attached from this process's own credential
// store and the destination comes from a closed template allowlist.
//
// Query carries the request's query parameters as key/value pairs rather than
// as a string, because a caller-supplied query STRING would be a second
// destination: this command encodes the pairs itself (encodePlatformQuery) and
// the encoder is what makes a smuggled second query or fragment impossible. Its
// values are typed `any` rather than `string` so that a non-string and a JSON
// null are both refused by one type assertion — decoded into a string, a JSON
// null is silently the empty string, which would admit a value the caller never
// sent.
type desktopPlatformRequest struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Params map[string]string `json:"params"`
	Query  map[string]any    `json:"query,omitempty"`
	Body   json.RawMessage   `json:"body,omitempty"`
}

// desktopPlatformResult carries every response shape this command can send: a status plus the
// platform's JSON body verbatim; a status plus the platform's content type and
// its body base64-encoded, for an answer that is not JSON; or a failure code
// from the declared vocabulary with no input echoed. It never carries a
// credential, and it never carries what the transport said — the codes in
// platformOutcomeCodes are this command's words, not the platform's.
//
// THE TWO SUCCESS ARMS ARE DISJOINT: the JSON arm carries body and no
// contentType or bodyBase64, and the binary arm carries contentType and
// bodyBase64 and no body. The Desktop wall keys its secret scan on the arm
// rather than on a type test, so an arm that carried both would put that scan
// over a byte payload.
//
// BodyBase64 is a pointer because the empty string is a legitimate binary body
// (a zero-byte CSV) and omitempty cannot tell it from an absent field: emitted
// as nothing, the binary arm would arrive at the Desktop wall missing the key
// its validator requires.
type desktopPlatformResult struct {
	State       string          `json:"state,omitempty"`
	Code        string          `json:"code,omitempty"`
	Status      int             `json:"status,omitempty"`
	ContentType string          `json:"contentType,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
	BodyBase64  *string         `json:"bodyBase64,omitempty"`
}

// WHICH PATHS THIS COMMAND ADMITS, and why the closed set is not in this file.
//
// Owner decision, 2026-09-13: the Desktop proxy admits every platform path the
// WEBSITE'S OWN API MODULES declare, and nothing else. That declaration is the
// agent repository's OpenAPI document — the schema the website's typed client
// is generated from — plus four hand-written fetch calls. It is derived rather than listed precisely so that a website
// module which starts calling a new operation is admitted without an edit to
// this command; a list here would have to be widened once per feature area as
// the parity program lands them, which is the failure the ruling replaces.
//
// THIS REPOSITORY HAS NO COPY OF THAT SCHEMA, and that is a fact rather than an
// omission: the knowledge client is not built from the agent's OpenAPI
// document and has no build-time or run-time access to it. So the CLOSED
// DERIVED SET is enforced where the artifact lives — in the Desktop, by
// desktop/platform.cjs over the generated desktop/platform-paths.cjs, before
// any subprocess is spawned — and THIS side enforces the STRUCTURE that set can
// take. Both walls refuse with path_not_allowed.
//
// What the structural wall is worth stating plainly: it admits a path the
// website could declare and refuses one it could not. A caller that reaches
// this command with a path outside the derived set but inside the structure
// gets the platform's own answer, which the platform authorizes against the
// bearer's rights — the same rights the web session has (owner: "the bearer
// token should have the same rights as the web session"). It never reaches a
// path outside the platform's own two route families, never escapes the
// account it asserted, and never carries a credential in either direction.
//
// A SECOND COPY OF THE DERIVED LIST HERE WOULD BE WORSE THAN THIS, not better:
// it could only be a hand-typed transcription of a generated file in another
// repository, pinned by a revision, and it would silently refuse a path the
// website had legitimately added — a stuck page with no diagnostic, which is
// the failure mode requirement 8 exists to prevent.
//
// platformPathFamilies are the platform's own route prefixes. /v1/ is the
// versioned API the schema describes; /auth/me is the one caller-scoped
// identity read, named exactly rather than by prefix so /auth/logout and
// /auth/login — navigations the website performs and this window cancels —
// are not admitted.
var platformPathFamilies = []string{"/v1/"}

const platformIdentityPath = "/auth/me"

// THE OUTCOME VOCABULARY IS ONE DECLARED SET, and platformFailure is the only
// place a member of it is constructed.
//
// A code this command emits and the Desktop's own validator does not admit is
// turned into platform_error at the Desktop wall — an unrelated outcome shown
// to the user for a request that failed for a stated reason. A declared set
// that some refusal site bypassed with a bare literal would be a list beside
// the behaviour rather than the behaviour's own vocabulary: the cross-wall
// agreement test would pass while the bypassing site emitted something the
// other wall refuses. So the set is declared here, every refusal names a
// member, and a source census (TestDesktopPlatformOutcomeVocabularyIsDeclared)
// is what keeps a re-inlined literal from surviving.
const (
	platformCodeInvalidRequest   = "invalid_request"
	platformCodePathNotAllowed   = "path_not_allowed"
	platformCodeUnauthenticated  = "unauthenticated"
	platformCodeUnreachable      = "platform_unreachable"
	platformCodeError            = "platform_error"
	platformCodeResponseTooLarge = "response_too_large"
)

// platformOutcomeCodes is the proxy's whole public failure vocabulary, the same
// set the Desktop's own validator admits (the `codes` list in
// desktop/platform.cjs). One declaration per side of the seam.
var platformOutcomeCodes = []string{
	platformCodeInvalidRequest,
	platformCodePathNotAllowed,
	platformCodeUnauthenticated,
	platformCodeUnreachable,
	platformCodeError,
	platformCodeResponseTooLarge,
}

// platformBodyCap is the largest raw platform body this command returns, on
// EITHER response arm. A body over it is refused with
// platformCodeResponseTooLarge and never truncated: a truncated CSV is a file
// the user would open and read as complete.
//
// The figure is the measured one. The platform's audit-log export is bounded by
// its own row limit, and at that limit a wide-field export is several MiB of
// raw CSV; parity forbids refusing on the Desktop what the website delivers, so
// the cap is above that and below the transport's own outer limit
// (auth.PlatformResponseLimit), which is what makes THIS the refusal an
// oversize answer meets. The cap applies to the JSON arm as well, because the
// transport's limit rose with it and a JSON answer should not have silently
// widened along the way.
const platformBodyCap = 12 << 20

// platformMethods is the method vocabulary. A method outside it is a malformed
// request. PATCH is admitted because the website's api modules call it.
var platformMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// platformSegment bounds every path segment, which is what makes a traversal or
// an injected query impossible rather than merely unlikely: no slash, no
// question mark, no hash, no percent, and never empty. The vocabulary admits
// the dot, the at sign and the underscore because platform identifiers carry
// them (an email actor, a versioned resource); it does not admit two dots in
// sequence, which the path check below refuses outright.
var platformSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@~-]{0,199}$`)

// platformParam bounds every template substitution. Same vocabulary as a
// segment: a substitution becomes one.
var platformParam = platformSegment

// ErrPlatformAction and ErrPlatformArguments are the two refusals
// DesktopPlatformCmd returns before it reads a request at all. They are
// sentinels rather than inline errors because the dispatch table's own test has
// to tell "routed to this command, which refused" from "never recognized", and
// identity is what says that: matching the rendered message would turn a
// rewording into a failure.
var (
	ErrPlatformAction    = errors.New("unsupported platform action")
	ErrPlatformArguments = errors.New("invalid platform arguments")
)

// desktopPlatformSelection binds the proxy's account selection to the
// configuration path the MAIN PROCESS supplied, never to the process-wide
// default. It is a named function rather than an argument inlined into the
// transport factory because the factory is production wiring a test replaces
// wholesale, and this binding is the part that must be pinned: a proxy reading
// the DEFAULT configuration path would stamp whatever account that file selects
// — a different account than the Desktop is showing, on a request the Desktop's
// own user made.
func desktopPlatformSelection(configPath string) *auth.AccountSelection {
	return auth.NewAccountSelection(configPath, time.Second)
}

// desktopPlatformTransport is the production wiring: the pinned cloud endpoint,
// the AuthKit credential source, and the selection above. The idiom is
// desktopAccountTransport's, in desktop_auth.go.
var desktopPlatformTransport = func(store auth.Store, configPath string) *auth.Transport {
	return auth.NewSyncTransport(CloudEndpoint, auth.NewOAuthTokenSource(store, CloudEndpoint, AllowedAuthHosts()), auth.WithAccountSelection(desktopPlatformSelection(configPath)))
}

// DesktopPlatformCmd forwards one platform API request for the Desktop
// renderer with the bearer attached from this process's credential store.
//
// The credential never crosses either boundary: the renderer supplies an
// intent (a method, an allowlisted path template, its substitutions, its query
// pairs and a JSON body) over stdin, and receives a status with either the
// platform's JSON body or its content type and base64-encoded body. Nothing
// in the request vocabulary can name a destination or carry a token, and
// nothing in the response vocabulary can carry one back. Public errors contain
// no input.
func DesktopPlatformCmd(args []string) error {
	// Bad configuration, refused before any work — see DesktopAuthCmd for why
	// this is a hard error rather than a JSON result code.
	if _, err := auth.CredentialNamespace(); err != nil {
		return err
	}
	if len(args) < 1 || args[0] != "request" {
		return ErrPlatformAction
	}
	flags := flag.NewFlagSet("desktop-platform", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config-file", "", "Knowledge configuration path")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*path) {
		return ErrPlatformArguments
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return json.NewEncoder(os.Stdout).Encode(runDesktopPlatform(ctx, *path, os.Stdin))
}

func platformFailure(code string) desktopPlatformResult {
	return desktopPlatformResult{State: "error", Code: code}
}

// runDesktopPlatform owns one proxied request: decode, refuse, resolve, send.
// Every refusal happens before the credential is read, so a malformed or
// off-allowlist request never touches the credential store.
func runDesktopPlatform(ctx context.Context, configPath string, input io.Reader) desktopPlatformResult {
	request, ok := decodePlatformRequest(input)
	if !ok {
		return platformFailure(platformCodeInvalidRequest)
	}
	// The method vocabulary is checked before the allowlist, because the two
	// refusals answer different questions: a method outside the enum is a
	// malformed request whatever path it named, while a method the enum carries
	// and the route does not is a call the allowlist does not admit.
	if !slices.Contains(platformMethods, request.Method) {
		return platformFailure(platformCodeInvalidRequest)
	}
	if !platformPathAdmitted(request.Path) {
		return platformFailure(platformCodePathNotAllowed)
	}
	// A body is admitted only where a write exists. A GET or DELETE carrying
	// one is a request the hosted surface never makes, so it is a malformed
	// request rather than a body to drop silently.
	if len(request.Body) > 0 && !slices.Contains([]string{http.MethodPost, http.MethodPut, http.MethodPatch}, request.Method) {
		return platformFailure(platformCodeInvalidRequest)
	}
	resolved, ok := resolvePlatformPath(request.Path, request.Params, configPath)
	if !ok {
		return platformFailure(platformCodeInvalidRequest)
	}
	// The query is validated and ENCODED here, and handed to the transport as
	// its own value rather than joined onto the path. The transport's path
	// guard refuses ".." and "//", and the filters the website sends include
	// free user text: an actor_email carrying two dots is a value the platform
	// accepts, and appending it to the path would turn it into a malformed path
	// reported as an unreachable platform.
	encoded, ok := encodePlatformQuery(request.Query)
	if !ok {
		return platformFailure(platformCodeInvalidRequest)
	}
	// FAIL CLOSED. No credential is no call: a signed-out or local-only Desktop
	// is told so, and nothing is sent unauthenticated in the hope of a public
	// answer.
	store, err := openStore()
	if err != nil {
		return platformFailure(platformCodeUnauthenticated)
	}
	refresh, err := store.Get(ctx, auth.KeyRefreshToken)
	if err != nil || refresh == "" {
		return platformFailure(platformCodeUnauthenticated)
	}
	account := ""
	if strings.Contains(request.Path, "{account_id}") {
		account = request.Params["account_id"]
	}
	answer, err := desktopPlatformTransport(store, configPath).PlatformRequest(ctx, auth.PlatformCall{
		Account: account,
		Method:  request.Method,
		Path:    resolved,
		Query:   encoded,
		Body:    request.Body,
	})
	if err != nil {
		// An answer the transport refused for its SIZE is reported as this
		// command's own named outcome. Reported as an unreachable platform —
		// which is what a bare transport error would become — a too-large
		// answer would tell the user the service is down.
		if errors.Is(err, auth.ErrPlatformResponseTooLarge) {
			return platformFailure(platformCodeResponseTooLarge)
		}
		// A selection the gateway has already rejected is reported as
		// unauthenticated rather than unreachable: the platform is fine and the
		// session as configured cannot authorize the call, which is the same
		// thing the shell's signed-out state says.
		if desktopSignInRequired(err) || errors.Is(err, auth.ErrAccountSelectionRejected) {
			return platformFailure(platformCodeUnauthenticated)
		}
		return platformFailure(platformCodeUnreachable)
	}
	// THE CAP IS CHECKED BEFORE THE ARM, and it applies to both: the raw body
	// is what crosses the seam, whether it crosses as JSON or as base64, and a
	// body over the cap is refused rather than truncated. A truncated CSV is a
	// file the user would open and read as complete.

	if len(answer.Body) > platformBodyCap {
		return platformFailure(platformCodeResponseTooLarge)
	}
	binary, ok := platformResponseArm(answer.ContentType)
	if !ok {
		return platformFailure(platformCodeError)
	}
	if binary {
		return platformBinaryResult(answer.Status, answer.ContentType, answer.Body)
	}
	result, ok := platformJSONResult(answer.Status, answer.Body)
	if !ok {
		return platformFailure(platformCodeError)
	}
	return result
}

// decodePlatformRequest reads the request under a size bound and refuses every
// key outside the vocabulary. The trailing-garbage check is what stops a second
// object riding the same stdin.
func decodePlatformRequest(input io.Reader) (desktopPlatformRequest, bool) {
	var request desktopPlatformRequest
	raw, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(raw) > 65536 {
		return request, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF {
		return request, false
	}
	return request, true
}

// platformPlaceholder matches one template placeholder, e.g. {account_id}.
var platformPlaceholder = regexp.MustCompile(`^\{[A-Za-z0-9_]+\}$`)

// platformPathAdmitted reports whether a path TEMPLATE has the shape a path the
// website's API modules declare can take. It is the structural wall described
// at the top of this file; the closed derived set is enforced Desktop-side,
// where the schema that defines it lives.
//
// Admitted: the caller-scoped identity read, named exactly; and any template
// under a platform route family whose every segment is either one safe segment
// or one placeholder. Refused: anything else — including /auth/logout and
// /auth/login, which the website reaches by NAVIGATING and this window cancels,
// so admitting them would forward a request whose answer nothing here can use.
//
// A traversal cannot survive this: a ".." segment fails platformSegment (which
// requires an alphanumeric first character), an empty segment fails it too, and
// a segment carrying a slash cannot exist after the split.
func platformPathAdmitted(path string) bool {
	if path == platformIdentityPath {
		return true
	}
	family := false
	for _, prefix := range platformPathFamilies {
		if strings.HasPrefix(path, prefix) {
			family = true
			break
		}
	}
	if !family {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) == 0 {
		return false
	}
	for _, part := range segments {
		if platformPlaceholder.MatchString(part) {
			continue
		}
		if !platformSegment.MatchString(part) {
			return false
		}
	}
	return true
}

// resolvePlatformPath substitutes the template's placeholders and asserts the
// account scope rather than trusting it.
//
// EVERY placeholder the template carries must have a parameter and every
// parameter must be a placeholder the template carries. A missing one would
// leave a literal brace in the path and an extra one would be a substitution
// the caller expected and did not get; both are the silent near-miss this
// check exists to prevent. The placeholder names are read FROM THE TEMPLATE
// rather than from a fixed list, because the admitted set is derived from a
// schema whose placeholder vocabulary this command does not enumerate.
//
// Where the template names an account, the caller's account_id must be the
// account the native state has selected: the renderer is hosted website code
// reading its own context, so its account is an assertion to check, never a
// scope to grant.
func resolvePlatformPath(template string, params map[string]string, configPath string) (string, bool) {
	names := platformPlaceholderNames(template)
	if len(params) != len(names) {
		return "", false
	}
	resolved := template
	for _, name := range names {
		value, present := params[name]
		if !present || !platformParam.MatchString(value) {
			return "", false
		}
		if name == "account_id" {
			selected, err := config.ReadSelectedAccountID(configPath)
			if err != nil || selected == "" || selected != value {
				return "", false
			}
		}
		resolved = strings.ReplaceAll(resolved, "{"+name+"}", value)
	}
	return resolved, true
}

// platformPlaceholderNames lists a template's placeholder names, deduplicated,
// in first-appearance order.
func platformPlaceholderNames(template string) []string {
	var names []string
	for part := range strings.SplitSeq(template, "/") {
		if !platformPlaceholder.MatchString(part) {
			continue
		}
		name := part[1 : len(part)-1]
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}
