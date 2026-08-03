// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// createProjectArgs is the complete set of create_project wire params — every
// field here is declared by CreateProjectToolDef and every declared property is
// read here, with no passthrough slots. The project's backend metadata
// (`backend`, `linear_id`, `external_url`, `linear_group_id`, `linear_group_key`)
// is NOT caller-suppliable: BuildProjectNode stamps it from the
// backends.RemoteRef and backends.Group that backend.CreateProject returned.
type createProjectArgs struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Summary     string `json:"summary"`
	Group       string `json:"group,omitempty"`
	Format      string `json:"format,omitempty"`
}

// InterceptCreateProject handles the create_project MCP call. When a
// backend is configured (provider.Default returns non-nil), the
// intercept runs the Linear create inline, then forwards the local
// portion to the server with the resulting backend metadata pre-baked
// into the wire args. When no backend is configured, returns
// (false, _) so the call falls through to the server's local-only path.
//
// Failure semantics (per locked design):
//   - backend.Groups error → returned as an MCP error result. No local node.
//   - backend.CreateProject error → returned as an MCP error result. No local node.
//   - server forward error → returned as an MCP error result. The remote
//     project DID land on Linear; the user sees a "remote succeeded,
//     local mirror failed — retry" error and can re-run safely (idempotent
//     under retry only if Linear's Create is idempotent on this payload,
//     which it is not; operator must reconcile manually).
func InterceptCreateProject(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "create_project" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("create_project: graph caller unavailable")
	}

	var a createProjectArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("create_project: invalid arguments: " + err.Error())
	}
	// Ahead of every validation and BEFORE any backend side-effect: the decode
	// above discards any top-level key createProjectArgs has no field for, so an
	// undeclared param would otherwise vanish into a successful create — and a
	// rejection that ran later could leave an orphan remote project behind.
	if err := rejectUndeclaredParams("create_project", "", CreateProjectToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if err := validate.Name("create_project", a.Name); err != nil {
		return true, errorResult(err.Error())
	}
	// Clamp an over-cap author summary (with a warning) BEFORE the backend
	// branch so BOTH the local mirror AND the Linear write receive the clamped
	// (<=500 rune) summary — keeping remote and local storage consistent.
	clamped, clampWarn, serr := validate.ClampSummary("create_project", "summary", a.Summary)
	if serr != nil {
		return true, errorResult(serr.Error())
	}
	a.Summary = clamped

	backend := deps.BackendResolver().Default()
	if backend == nil {
		// No backend configured — compose the local-only project node
		// client-side. Server-side handleCreateProject
		// now has no server-side dispatch so we MUST claim this case.
		return true, createProjectLocalOnly(ctx, gc, a, clampWarn)
	}

	ref, group, err := buildAndPushProjectToLinear(ctx, backend, a)
	if err != nil {
		return true, errorResult("create_project: " + err.Error())
	}

	// Compose the local-graph mirror with backend metadata stamped onto
	// the project node via BuildProjectNode. PersistBatch + bundle_id
	// replaces the previous gc.Call("create_project", ...) forward —
	// handleCreateProject was stubbed server-side.
	projectArgs := projects.ProjectArgs{
		Name:        a.Name,
		Description: a.Description,
		Summary:     a.Summary,
		BackendName: backend.Name(),
		RemoteRef:   ref,
		RemoteGroup: group,
	}
	node, edges := projects.BuildProjectNode(projectArgs, projectArgs.BackendName, projectArgs.RemoteRef, projectArgs.RemoteGroup)
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{node}, edges, bundleID)
	if perr != nil {
		return true, errorResult(fmt.Sprintf(
			"create_project: Linear create succeeded for %q (remote_id=%s, url=%s) but local mirror failed: %v",
			a.Name, ref.ID, ref.URL, perr,
		))
	}
	if len(ids) == 0 {
		return true, errorResult("create_project: persist returned no IDs")
	}
	projectID := ids[0]
	var warnings []string
	if clampWarn != "" {
		warnings = append(warnings, clampWarn)
	}
	if a.Format == "json" {
		return true, jsonResult(map[string]any{
			"id":       projectID,
			"name":     a.Name,
			"warnings": orNilWarnings(warnings),
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Project created: %s → ID: %s", a.Name, projectID)
	writeClientWarningsSection(&sb, warnings, "\n")
	return true, textResult(sb.String() + " [graph: knowledge/default]")
}

// createProjectLocalOnly composes a local-only project node (no
// backend write-through) and persists it via PersistBatch under one
// bundle_anchor. Server-side handleCreateProject's local-only branch
// was stubbed so the client owns this path now. clampWarn carries a
// non-empty warning when the author summary was clamped, surfaced in the
// result so the local-only path reports the clamp like the backend path.
func createProjectLocalOnly(ctx context.Context, gc GraphCaller, a createProjectArgs, clampWarn string) kgtools.ToolResult {
	projectArgs := projects.ProjectArgs{
		Name:        a.Name,
		Description: a.Description,
		Summary:     a.Summary,
	}
	node, edges := projects.BuildProjectNode(projectArgs, "", backends.RemoteRef{}, backends.Group{})
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{node}, edges, bundleID)
	if perr != nil {
		return errorResult("create project: " + perr.Error())
	}
	if len(ids) == 0 {
		return errorResult("create project: persist returned no IDs")
	}
	projectID := ids[0]
	var warnings []string
	if clampWarn != "" {
		warnings = append(warnings, clampWarn)
	}
	if a.Format == "json" {
		return jsonResult(map[string]any{
			"id":       projectID,
			"name":     a.Name,
			"warnings": orNilWarnings(warnings),
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Project created: %s → ID: %s", a.Name, projectID)
	writeClientWarningsSection(&sb, warnings, "\n")
	return textResult(sb.String() + " [graph: knowledge/default]")
}

// buildAndPushProjectToLinear is the pure-function inner of
// InterceptCreateProject: resolve the group, then call
// backend.CreateProject. Returns the remote ref + the resolved group so
// the caller can stamp both onto the forwarded args.
func buildAndPushProjectToLinear(
	ctx context.Context,
	backend backends.Backend,
	a createProjectArgs,
) (backends.RemoteRef, backends.Group, error) {
	groups, err := backend.Groups(ctx)
	if err != nil {
		return backends.RemoteRef{}, backends.Group{}, fmt.Errorf("list backend groups: %w", err)
	}
	group, err := resolveBackendGroup(a.Group, groups)
	if err != nil {
		return backends.RemoteRef{}, backends.Group{}, err
	}
	ref, err := backend.CreateProject(ctx, backends.ProjectCreateArgs{
		GroupKey:    group.Key,
		Name:        a.Name,
		Summary:     a.Summary,
		Description: a.Description,
		Status:      "",
		Priority:    0,
		Labels:      "",
	})
	if err != nil {
		// Don't re-wrap with "<backend>: ..." — the backend adapter
		// already names itself in its error chain.
		return backends.RemoteRef{}, backends.Group{}, err
	}
	return ref, group, nil
}

// resolveBackendGroup picks the backends.Group to use for a backend
// create. Port of the historical projects/backend.go::resolveGroup
// (relocated rather than rewritten to preserve git blame line
// alignment). Same four cases, same error texts.
//
// Cases:
//  1. requested != "" AND found in groups → use it.
//  2. requested != "" AND NOT found → error listing available keys.
//  3. requested == "" AND len(groups) == 0 → error.
//  4. requested == "" AND len(groups) == 1 → auto-default to the one.
//  5. requested == "" AND len(groups) > 1 → error listing alternatives.
func resolveBackendGroup(requested string, groups []backends.Group) (backends.Group, error) {
	if requested != "" {
		for _, g := range groups {
			if g.Key == requested {
				return g, nil
			}
		}
		return backends.Group{}, fmt.Errorf("group %q not found; available: %s", requested, joinBackendGroupKeys(groups))
	}
	switch len(groups) {
	case 0:
		return backends.Group{}, fmt.Errorf("no groups available from backend; cannot create project")
	case 1:
		return groups[0], nil
	default:
		return backends.Group{}, fmt.Errorf(
			"group not specified and %d groups available: %s; pass group=<key>",
			len(groups), joinBackendGroupKeys(groups),
		)
	}
}

// joinBackendGroupKeys returns a comma-space joined list of Group.Key
// values, in the order they appear. Used by resolveBackendGroup error
// messages so the operator sees the same shape regardless of which
// branch produced the error.
func joinBackendGroupKeys(groups []backends.Group) string {
	keys := make([]string, len(groups))
	for i, g := range groups {
		keys[i] = g.Key
	}
	return strings.Join(keys, ", ")
}
