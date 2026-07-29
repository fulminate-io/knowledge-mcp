// SPDX-License-Identifier: Apache-2.0

package graphclient

import "context"

// Operation is the client-side query-origin vocabulary term naming WHAT the
// client is doing when it issues an RPC. It is a DEFINED TYPE rather than a
// bare string on purpose: the ctx setter takes an Operation, so a stray literal
// at a call site is a compile error instead of an unbounded metrics label that
// only a test could hope to catch. The declared terms live in operation_vocab.go
// and are the ONLY thing bounding this dimension's cardinality — the server
// validates the term's SHAPE but deliberately keeps no closed list, so that
// adding a client operation never requires a server release.
type Operation string

// operationKey is the typed context key carrying the client operation through
// the outbound call stack to the stamping interceptor. An unexported zero-size
// struct so the key cannot collide with any other package's context value —
// the same shape the server uses for its own per-request ctx values.
type operationKey struct{}

// WithOperation returns a copy of ctx carrying the client operation. An empty
// operation is a no-op: the returned ctx is the original unchanged, so
// OperationFromContext still reports ("", false). Keeping "no operation in
// context" unambiguous is what lets the interceptor tell a genuinely unstamped
// call path apart from one that stamped an empty string.
func WithOperation(ctx context.Context, op Operation) context.Context {
	if op == "" {
		return ctx
	}
	return context.WithValue(ctx, operationKey{}, op)
}

// OperationFromContext extracts the client operation from ctx. The second
// return is false when no (non-empty) operation was attached — the signal the
// stamping interceptor maps to its default-deny handling. Symmetric reader for
// WithOperation.
func OperationFromContext(ctx context.Context) (Operation, bool) {
	v, ok := ctx.Value(operationKey{}).(Operation)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
