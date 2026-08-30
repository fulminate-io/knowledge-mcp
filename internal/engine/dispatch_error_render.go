// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// dispatch_error_render.go holds the transport-error render: the mapping ladder
// that turns an Engine.Execute failure into an LLM-facing message, its
// local-server-unreachable predicate, and the two empty-message arms.
//
// SPLIT OUT OF dispatch.go on that file's own sibling-split convention, purely to
// stay inside the repo's 500-line file convention: the empty-message arms pushed
// it past the cap. Nothing about the ladder changed in the move — read this file
// and dispatch.go as one unit, since Dispatch is renderEngineError's only caller.

// renderEngineError maps an error returned by the Engine.Execute transport
// into an LLM-facing error ToolResult. The mapping ladder, in order:
//
//  1. graphclient.ErrNoBackend — Router.pick returned no backend
//     (local==nil AND not logged in). Surfaces "no backend available — run
//     `knowledge install` ... `knowledge login`" so the LLM has an
//     actionable hint rather than the raw sentinel text.
//  2. Local-server-unreachable transport failure — connect-go's
//     CodeUnavailable, or a wrapped syscall.ECONNREFUSED in a net.OpError
//     chain. The local server was named (Router.pick chose it) but the
//     underlying HTTP/2 dial failed. Surfaces "local server unreachable —
//     run `knowledge start` ... `knowledge login`" so the user sees the
//     restart hint, not a raw "connect: connection refused" leak.
//  3. CodeInvalidArgument / CodeNotFound — engine validation / missing-node
//     errors. Message relayed verbatim (legacy behavior).
//  4. Any other connect.Error — generic "engine: <message>" fallback.
//  5. Non-connect errors — generic "engine: <error>" fallback.
//
// Ordering matters: branches (1) and (2) MUST fire before the generic connect
// switch because CodeUnavailable would otherwise land in the default arm with
// the raw "connect: connection refused" leak.
func renderEngineError(err error) kgtools.ToolResult {
	if errors.Is(err, graphclient.ErrNoBackend) {
		return errorResult("no backend available — run `knowledge install` to start the local server, or `knowledge login` to use Fulminate Cloud.")
	}
	if isLocalServerUnreachable(err) {
		return errorResult("local server unreachable — run `knowledge start` to restart it, or `knowledge login` to use Fulminate Cloud.")
	}
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		switch ce.Code() {
		case connect.CodeInvalidArgument, connect.CodeNotFound:
			return errorResult(ce.Message())
		default:
			if msg := strings.TrimSpace(ce.Message()); msg == "" {
				return errorResult(emptyConnectMessage(ce.Code()))
			}
			return errorResult("engine: " + ce.Message())
		}
	}
	if msg := strings.TrimSpace(err.Error()); msg == "" {
		return errorResult(emptyGoErrorMessage(err))
	}
	return errorResult("engine: " + err.Error())
}

// emptyConnectMessage and emptyGoErrorMessage render the two arms above when the
// underlying message is EMPTY. Without them both arms emitted exactly "engine: "
// — a body whose entire content is the prefix, which tells a reader nothing at
// all and is the shape 49 measured tool results carried.
//
// THEY NAME WHAT IS KNOWN AND NOTHING ELSE. A connect error always carries a
// CODE even when its message is empty, and a bare Go error always has a concrete
// TYPE, so each says which it is and states plainly that the responder returned
// no diagnostic — locating the emptiness upstream rather than in the client.
// Guessing a cause from an empty message ("connection reset") would be
// manufacturing a diagnosis; reporting the inability truthfully is the correct
// output here.
//
// THEY SIT INSIDE THE GENERIC ARMS, BELOW THE LADDER. Hoisting an emptiness
// check above branches (1) and (2) would swallow the two actionable messages —
// the ordering hazard this function's header states.
func emptyConnectMessage(code connect.Code) string {
	return fmt.Sprintf("engine: the server returned %s with no diagnostic message. "+
		"The code is all that came back, so the detail is missing upstream rather than dropped here.", code)
}

func emptyGoErrorMessage(err error) string {
	return fmt.Sprintf("engine: a %T failed with an empty error message. "+
		"The type is all that came back, so the detail is missing at its source rather than dropped here.", err)
}

// isLocalServerUnreachable reports whether err carries the
// local-server-unreachable signature: either a connect.Error with
// CodeUnavailable (the connect-go transport surfaces this when the underlying
// HTTP/2 dial fails) OR a wrapped syscall.ECONNREFUSED (the underlying connect
// error sometimes presents as a net.OpError carrying ECONNREFUSED — errors.Is
// catches the wrapped case). Mirrors the retryable-transport classification in
// graphclient/retry.go for the dispatcher's render path.
func isLocalServerUnreachable(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce.Code() == connect.CodeUnavailable
	}
	return false
}
