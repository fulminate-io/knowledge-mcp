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

// workspaceCwdKey is the typed context key carrying a session's resolved
// workspace cwd through the tool dispatch call stack (HTTP transport). A
// private type avoids cross-package collisions, mirroring sessionIDKey.
type workspaceCwdKey struct{}

// ContextWithWorkspaceCwd returns a copy of ctx with the per-session workspace
// cwd attached. An empty cwd is a no-op (returns ctx unchanged) so the stdio
// path — which carries no workspace cwd — leaves the context clean and repo
// resolution falls back to the process --root.
func ContextWithWorkspaceCwd(ctx context.Context, cwd string) context.Context {
	if cwd == "" {
		return ctx
	}
	return context.WithValue(ctx, workspaceCwdKey{}, cwd)
}

// WorkspaceCwdFromContext extracts the per-session workspace cwd from ctx.
// Returns "" if none was set (the stdio default), signaling callers to fall
// back to the process --root.
func WorkspaceCwdFromContext(ctx context.Context) string {
	v, _ := ctx.Value(workspaceCwdKey{}).(string)
	return v
}
