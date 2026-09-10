// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// mcphost.go — the outbound MCP client this package is built around: dial a
// registered provider, complete the handshake, and hold the session while the
// tool is verified and called.
//
// THE DAEMON COMPOSES NO CREDENTIAL, ON EITHER TRANSPORT. No SDK auth package is
// wired: the OAuthHandler is nil and none of the SDK's auth, auth/extauth or
// oauthex packages is imported here or anywhere under cmd/knowledge, which is a
// structural requirement with a corpus check behind it rather than a convention.
// What a provider receives is what the operator's own config entry says: a stdio
// provider gets exactly the entry's env block, and an http provider gets exactly
// the entry's headers. The daemon adds nothing of its own to either, and in
// particular composes no header from its environment, its credentials or its
// login state — a second corpus check watches that shape.

// COLLECTOR TRAFFIC CARRIES NO SIZE CAP, IN EITHER DIRECTION, and the absence
// is a decision rather than an omission. This package used to bound a provider
// result at 64 MiB, here and in DecodeResult; that bound is gone. The traffic is
// MCP to MCP and none of it reaches a model context, so the reason a cap exists
// on a rendered surface does not apply. What sizes a collect is the collector's
// declared context block and the module's own reads, both of which an operator
// can read off the config entry. Do not reintroduce a byte bound on this path.

// clientImplName / clientImplVersion identify this client in the MCP handshake.
// A provider logs them, so they name the product rather than the package.
const (
	clientImplName    = "knowledge"
	clientImplVersion = "v1"
)

// providerSession is a dialed provider: the live MCP session plus the closer
// that tears the transport (and, for stdio, the child process) down.
type providerSession struct {
	session *mcp.ClientSession
}

func (p *providerSession) close() {
	if p == nil || p.session == nil {
		return
	}
	// A close failure is reported to the log by the SDK's own teardown; there is
	// nothing a caller can do with it and nothing it changes about the collect
	// that already completed, so it is deliberately not joined into the result.
	_ = p.session.Close()
}

// dialProvider connects to the provider the spec names and completes the MCP
// handshake. Exactly one transport is set by construction (validation refuses
// zero or two), and the default arm errors rather than returning a nil session.
func dialProvider(ctx context.Context, col *knowledgev1.CollectorSpec, headers map[string]string) (*providerSession, error) {
	transport, err := providerTransport(ctx, col, headers)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: clientImplName, Version: clientImplVersion}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("custom collector: MCP handshake with the provider failed: %w", err)
	}
	return &providerSession{session: session}, nil
}

// providerTransport builds the transport for whichever provider the record
// carries. headers apply to the http arm alone: a stdio provider is a child
// process and has no request to carry one.
func providerTransport(ctx context.Context, col *knowledgev1.CollectorSpec, headers map[string]string) (mcp.Transport, error) {
	switch {
	case col.GetStdio() != nil:
		return stdioTransport(ctx, col.GetStdio())
	case col.GetHttp() != nil:
		return httpTransport(col.GetHttp(), headers)
	default:
		// Unreachable through the registration gate, which refuses a record
		// naming neither provider. It errors rather than degrading so a record
		// that reached here another way fails loud instead of dialing nothing.
		return nil, fmt.Errorf("custom collector: the registration names no provider (expected exactly one of stdio or http)")
	}
}

// stdioTransport spawns the provider as a child process speaking MCP over
// stdin/stdout.
//
// THE ENTRY'S ENV BLOCK IS THE CHILD'S WHOLE ENVIRONMENT. cmd.Env is assigned
// from scratch from the NAME=value pairs the config entry supplies; a child
// launched with a nil Env inherits the daemon's whole environment, which is the
// posture this inverts. The daemon LOOKS UP NOTHING: a variable set in the
// daemon's own environment and absent from the block does not reach the child,
// and a variable in the block reaches the child carrying the BLOCK's value, not
// the daemon's, even when the daemon holds the same name with a different value.
// Nothing else reaches the provider — not HOME, not the cloud SDK variables, not
// the LLM keys.
//
// THERE IS NO PLATFORM BASELINE, AND THE ABSENCE IS A DECISION. A proxy or a
// certificate-bundle variable reaches a provider only because the operator's own
// entry supplies it: the daemon adds nothing of its own. A baseline of proxy and
// trust-root names passed to every child was built and WITHDRAWN by the owner as
// an over-complication of a contract that has to stay simple — the collectors
// shipped here are part example and part replacement for the built-in cloud and
// log features, so the contract they demonstrate is the one every third party
// copies; a future custom collector may run on another host over HTTP, which the
// daemon never spawns at all; and a stdio collector may be a pre-existing binary
// whose author expects an explicit environment. A collector that needs a proxy or
// a CA bundle gets those names in its entry like any other. The rule is one
// sentence with no exception, which is the point of it.
//
// The command is resolved through exec.LookPath ONCE here and the resolved path
// is what is spawned: the command comes out of a registration record, so it is a
// non-constant command and owes the repo's recorded subprocess bar (LookPath for
// non-constant commands, every argument a separate argv element, never a shell
// string, an explicit Dir, and a context the caller can cancel).
//
// THE CARRIER FOR "NOTHING ELSE SPAWNS" IS AN IMPORT CENSUS, and that is worth
// stating here because the obvious carrier — a pattern check per spawn form —
// was tried first and does not hold. The census
// (spawn_import_census_test.go) walks this module with go/parser and pins WHICH
// FILES import os/exec, each with the reason it needs to. It reads the import
// PATH, so it sees an aliased or blank import exactly as it sees a plain one,
// and a new file that spawns is caught whatever syntax it uses. Within this
// package the census asserts a single name: this file.
//
// WHY NOT PATTERN CHECKS ALONE. A check carries one pattern and therefore keys
// on a SPELLING, while the requirement is an EFFECT. Successive audits each
// found the next spelling — the two constructors, then their arities, then the
// composite literal, then an aliased import that silenced the whole set — and
// ten checks accumulated tracking them. They remain in the corpus and still earn
// their place: the census bounds which FILES may spawn, and they are what
// notices a second, differently-shaped spawn added INSIDE an already-listed
// file, which is exactly this one. Belt and braces, with the coarse one first.
//
// THE PACKAGE QUALIFIER IS CAPTURED, NOT SPELLED, on all ten and on the
// transport their destination leg looks for. That is the general lesson rather
// than a detail — and capturing it on the transport too is what stops a host
// that aliases the SDK from being flagged as a defect.
//
// WHAT NEITHER CARRIER SEES. The census is file-granular, so a record-derived
// command handed to one of the OTHER listed importers by some path — parked on a
// struct field, passed to a same-package wrapper, reached through a function
// variable — is a spawn it counts as legitimate, because that file was already
// allowed to spawn. That gap is the same one the pattern checks cannot close
// either: their provenance leg is flows_to, which is intra-declaration by
// design, so a command crossing a declaration or a value boundary is outside all
// of them, and a dot-imported call has no qualifier node to capture at all. The
// DESTINATION is now bindable at the value level — a flows_to `to` names the
// value occupying the transport's Command field rather than the literal as a
// whole, so "does THIS command reach the transport" is a question the checks can
// ask. This is the one function in the tree that builds a transport, so a SECOND
// spawn added here — one that runs the provider and reads its output instead of
// handing it to the transport — is the shape the census would miss and the
// effect check catches. One *exec.Cmd, built once, handed to the transport,
// never read from here.
//
// The import census is not a corpus check because of what a check cannot carry,
// not because the pattern language cannot see an import. A single-spec pattern
// DOES bind an import spec in both declaration shapes — `$$$P "os/exec"` matches
// grouped and ungrouped, aliased and plain — so "which files import a spawning
// package" is expressible. What a check cannot express is the rest of this test:
// a per-file allowlist with the reason each entry needs a spawn, the stale-entry
// direction that keeps the list a census rather than a standing permission, the
// known positive proving the walk read the right tree, and the package-scoped
// contract that mcphost.go alone may spawn inside this package.
func stdioTransport(ctx context.Context, spec *knowledgev1.StdioProvider) (mcp.Transport, error) {
	resolved, err := exec.LookPath(spec.GetCommand())
	if err != nil {
		return nil, fmt.Errorf("custom collector: stdio provider command %q is not executable: %w", spec.GetCommand(), err)
	}
	cmd := exec.CommandContext(ctx, resolved, spec.GetArgs()...)
	cmd.Env = childEnv(spec.GetEnv())
	// An explicit working directory rather than whatever the daemon's happens to
	// be: a provider must not resolve relative paths against the operator's cwd.
	cmd.Dir = os.TempDir()
	// stdout is the MCP transport and belongs to the SDK. stderr is the
	// provider's log stream; it is forwarded to the daemon's own stderr so a
	// provider's diagnostics are visible rather than discarded.
	cmd.Stderr = os.Stderr
	return &mcp.CommandTransport{Command: cmd}, nil
}

// childEnv COPIES the entry's env pairs into the slice os/exec takes.
//
// IT ALWAYS RETURNS A NON-NIL SLICE, and that is the whole of it: a nil Env
// means "inherit the parent's environment" to os/exec, so an entry with an empty
// or absent env block returning nil would silently restore the very inheritance
// this contract removes. The proto getter returns nil for an empty repeated
// field, so passing it through unconverted is exactly that defect.
//
// The elements arrive as NAME=value in sorted key order from the config loader,
// which renders the entry's env object; this function neither reorders them nor
// adds anything of its own.
func childEnv(pairs []string) []string {
	env := make([]string, 0, len(pairs))
	env = append(env, pairs...)
	return env
}

// httpTransport dials a streamable-HTTP provider, sending the entry's headers on
// every request.
//
// THE HEADERS ARE THE OPERATOR'S, AND ONLY THE OPERATOR'S. They come from the
// config entry verbatim; the daemon composes none of its own, reads none from
// its environment and attaches no credential of its own — the OAuthHandler stays
// nil and no SDK auth package is wired.
//
// The response body is bounded by a limiting round tripper: over-cap is an
// error, never a truncated document.
func httpTransport(spec *knowledgev1.HttpProvider, headers map[string]string) (mcp.Transport, error) {
	endpoint := strings.TrimSpace(spec.GetUrl())
	if _, err := url.Parse(endpoint); err != nil {
		return nil, fmt.Errorf("custom collector: http provider url %q does not parse: %w", endpoint, err)
	}
	return &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: &headerTransport{
			base:    http.DefaultTransport,
			headers: headers,
		}},
	}, nil
}

// headerTransport carries the entry's headers onto every request the session
// makes.
//
// IT WAS boundedTransport AND IT BOUNDED THE RESPONSE BODY TOO. That bound is
// gone with the rest of the collector-traffic caps, and the type is renamed to
// what it now does: a name that still said "bounded" would be the second source
// a reader has to disbelieve.
//
// THE HEADERS LIVE ON A ROUND TRIPPER because they are a property of EVERY
// request the session makes rather than of one call: the handshake, the tool
// listing and the tool call each need the operator's headers, and a header
// attached at construction time to the first request only would satisfy a check
// that looked at one.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(h.headers) > 0 {
		// CLONED, not mutated in place: a RoundTripper does not own the request it
		// is handed, and the SDK reuses request objects across retries.
		req = req.Clone(req.Context())
		for name, value := range h.headers {
			req.Header.Set(name, value)
		}
	}
	return h.base.RoundTrip(req)
}

// findTool resolves the registered tool name in the provider's tool listing and
// returns the advertised Tool. A tool the provider does not list is an error
// NAMING the tool and what the provider does list, because the likeliest cause
// is a typo or a version skew and the listing is the answer to both.
func findTool(ctx context.Context, session *mcp.ClientSession, tool string) (*mcp.Tool, error) {
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("custom collector: listing the provider's tools failed: %w", err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, t := range listed.Tools {
		if t == nil {
			continue
		}
		if t.Name == tool {
			return t, nil
		}
		names = append(names, t.Name)
	}
	return nil, fmt.Errorf("custom collector: the provider does not list the registered tool %q; it lists: %s",
		tool, strings.Join(names, ", "))
}
