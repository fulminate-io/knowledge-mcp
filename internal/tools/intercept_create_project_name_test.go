// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// The project-name length guard at the create_project intercept, split out of
// intercept_create_project_test.go to keep both files under the repository's
// 500-line ceiling. The shared doubles (fakeBackend, fakeResolver,
// interceptTestDeps) still live there.

// runes80 builds a name of exactly n ASCII runes for the name-cap tests.
func runesN(n int) string { return strings.Repeat("a", n) }

// TestInterceptCreateProject_NameOverCap_RefusedBeforeBackend — R4 at the
// path level. A create_project whose name exceeds the 80-rune cap is refused
// with the limit named, and the refusal lands BEFORE the backend branch, so
// no remote project and no local node exist afterwards. The name here is 131
// runes: the paired control in intercept_create_ticket_test.go sends a name
// of the SAME length to create_ticket and requires it to CREATE, which is
// what separates the settled reading (the cap is on the create_project path)
// from the literal one (the cap inside validate.Name, reaching all ten of its
// callers including create_ticket's).
func TestInterceptCreateProject_NameOverCap_RefusedBeforeBackend(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	name := runesN(131)
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"` + name + `","description":"d","summary":"s","group":"FUL"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError, "an over-cap project name must be refused")
	body := toolResultText(res)
	assert.Contains(t, body, "80", "the refusal must name the limit")
	assert.Contains(t, body, "create_project", "the refusal must name the calling tool")
	assert.Equal(t, 0, fb.createProjectCalls, "no remote project may be created on a refusal")
	assert.Equal(t, 0, fb.groupsCalls, "the refusal must land before the backend branch")
	assert.Empty(t, fc.execMutations, "no local node may be persisted on a refusal")
}

// TestInterceptCreateProject_NameAtCap_Creates — the boundary's other side:
// exactly 80 runes passes and reaches the backend. Without it the test above
// would also pass against a guard that refused every name.
func TestInterceptCreateProject_NameAtCap_Creates(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	name := runesN(80)
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"` + name + `","description":"d","summary":"s","group":"FUL"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "a name of exactly 80 runes must pass: %s", toolResultText(res))
	assert.Equal(t, 1, fb.createProjectCalls)
	assert.Equal(t, name, fb.createProjectArg.Name, "nothing is clamped — the backend gets the caller's name verbatim")
}

// TestInterceptCreateProject_PaddedNameOverCap_RefusedBeforeBackend — the
// padded input class at the path level. The intercept passes the caller's name
// to the backend VERBATIM (buildAndPushProjectToLinear sets Name: a.Name), so
// surrounding whitespace is part of what the tracker measures. A name whose
// body is exactly at the cap but which is over the cap once its padding is
// counted must be refused here, not discovered at the tracker.
func TestInterceptCreateProject_PaddedNameOverCap_RefusedBeforeBackend(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	name := "   " + runesN(80) + "   " // 86 runes as sent
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"` + name + `","description":"d","summary":"s","group":"FUL"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError,
		"a name that is over the cap once its padding is counted must be refused: the tracker receives the padding too")
	assert.Contains(t, toolResultText(res), "80", "the refusal must name the limit")
	assert.Equal(t, 0, fb.createProjectCalls, "no remote project may be created on a refusal")
	assert.Equal(t, 0, fb.groupsCalls, "the refusal must land before the backend branch")
	assert.Empty(t, fc.execMutations, "no local node may be persisted on a refusal")
}

// TestInterceptCreateProject_PaddedNameWithinCap_ReachesBackendVerbatim — the
// other side of the class, and the reason the guard refuses rather than trims:
// a padded name that FITS is sent with its padding intact. Nothing normalises
// the caller's name on the way to the tracker, here or anywhere between.
func TestInterceptCreateProject_PaddedNameWithinCap_ReachesBackendVerbatim(t *testing.T) {
	fb := &fakeBackend{
		groupsResult:     []backends.Group{{Key: "FUL", ID: "team-uuid"}},
		createProjectRef: backends.RemoteRef{ID: "proj-uuid", URL: "https://example.invalid/p"},
	}
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{backend: fb, gc: fc}
	name := " " + runesN(70) + " " // 72 runes as sent, under the cap
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"` + name + `","description":"d","summary":"s","group":"FUL"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "a padded name under the cap must pass: %s", toolResultText(res))
	assert.Equal(t, name, fb.createProjectArg.Name,
		"the name reaches the backend verbatim, padding included — this guard refuses, it never trims")
}

// TestInterceptCreateProject_LocalOnlyNameOverCap_RefusedNoNodePersisted —
// THE NO-BACKEND CLASS, which every other R4 path test misses because they all
// configure a backend.
//
// The guard runs ahead of the backend branch, so it governs the local-only arm
// too: with no backend configured, an over-cap name must still be refused and
// no project node may be persisted. That parity is the reason the guard sits
// where it does, and without this test moving it below the nil-backend early
// return leaves every other R4 test green while a 131-rune local-only create
// succeeds and writes a node.
//
// The local-only arm is the OSS default — a user with no tracker configured
// takes it on every create_project — so this is the arm most installations run.
func TestInterceptCreateProject_LocalOnlyNameOverCap_RefusedNoNodePersisted(t *testing.T) {
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{gc: fc} // no backend configured
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"` + runesN(131) + `","description":"d","summary":"s"}`),
	})
	require.True(t, handled)
	require.True(t, res.IsError, "an over-cap name must be refused with no backend configured too: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "80", "the refusal must name the limit")
	assert.Empty(t, fc.execMutations, "no project node may be persisted on a refusal")
}

// TestInterceptCreateProject_LocalOnlyNameAtCap_Creates — the boundary's other
// side on the same arm, so the test above cannot pass against a guard that
// refused every local-only create.
func TestInterceptCreateProject_LocalOnlyNameAtCap_Creates(t *testing.T) {
	fc := &fakeGraphCaller{mutateResult: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["proj-local-id"]}`}},
	}}
	deps := interceptTestDeps{gc: fc}
	handled, res := InterceptCreateProject(opCtx(), deps, kgtools.CallToolParams{
		Name:      "create_project",
		Arguments: json.RawMessage(`{"name":"` + runesN(80) + `","description":"d","summary":"s"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "a name of exactly 80 runes must create on the local-only arm: %s", toolResultText(res))
	assert.Len(t, fc.execMutations, 1, "the local-only create must persist its node")
}
