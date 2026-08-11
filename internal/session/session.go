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

// harnessSessionIDKey is the typed context key carrying a session's resolved
// HARNESS session-id — the daemon-derived identity read from the agent's
// on-disk transcript, never supplied by the agent. A private type avoids
// cross-package collisions, mirroring sessionIDKey.
//
// It is a DIFFERENT value from the MCP session-id above with a different
// lifetime: the MCP session-id is a local identity (ban-gate correlation, claim
// registry, hive-active marking) while the harness session-id is the identity
// the cloud keys a hive member on. They deliberately occupy separate slots.
type harnessSessionIDKey struct{}

// ContextWithHarnessSessionID returns a copy of ctx with the harness session-id
// attached. An empty id is a no-op (returns ctx unchanged) so a session whose
// transcript has not resolved leaves the context clean and the carrier stamps
// no header.
func ContextWithHarnessSessionID(ctx context.Context, harnessID string) context.Context {
	if harnessID == "" {
		return ctx
	}
	return context.WithValue(ctx, harnessSessionIDKey{}, harnessID)
}

// HarnessSessionIDFromContext extracts the harness session-id from ctx. Returns
// "" if none was set — the unresolved-transcript state.
func HarnessSessionIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(harnessSessionIDKey{}).(string)
	return v
}
