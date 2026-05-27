// SPDX-License-Identifier: Apache-2.0

// Package session provides the typed-context helpers used to carry a session
// ID through the tool dispatch call stack on the client side.
package session

import "context"

// sessionIDKey is the typed context key used to carry a session ID through the
// tool dispatch call stack. Using a private type avoids collisions with other
// packages that might store values in context.
type sessionIDKey struct{}

// ContextWithSessionID returns a copy of ctx with the session ID attached.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFromContext extracts the session ID from ctx. Returns "" if none was set.
func SessionIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDKey{}).(string)
	return v
}
