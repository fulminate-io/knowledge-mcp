// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// Backend implements backends.Backend against api.linear.app.
//
// Construct via New() (env-var path) or as a struct literal (test path with
// an injected *Client). The compile-time assertion that Backend satisfies
// backends.Backend lands in backend_write.go (Phase 4) once all 9 methods
// are implemented; adding it here would fail to build.
type Backend struct {
	Client *Client
}

// New constructs a production Linear Backend reading the Linear key via
// config.LinearAPIKey — the [credentials].linear_api_key field in
// ~/.knowledge/config if set, otherwise the LINEAR_API_KEY env var.
// Provider in domains/backends/provider.go calls this when Enabled()
// returns true.
func New() *Backend {
	return &Backend{Client: NewClient()}
}

// Name returns the stable backend identifier. T2 stores this on local nodes
// as the `backend` metadata key; T3 routes per-node updates by it.
func (b *Backend) Name() string { return "linear" }

// Groups lists every team the API key can see. Maps Linear team → Group:
// team.id → Group.ID, team.key → Group.Key, team.name → Group.Name.
func (b *Backend) Groups(ctx context.Context) ([]backends.Group, error) {
	var resp teamsResponse
	if err := b.Client.do(ctx, teamsQuery, nil, &resp); err != nil {
		return nil, fmt.Errorf("linear: list teams: %w", err)
	}
	out := make([]backends.Group, 0, len(resp.Teams.Nodes))
	for _, t := range resp.Teams.Nodes {
		out = append(out, backends.Group{ID: t.ID, Key: t.Key, Name: t.Name})
	}
	return out, nil
}

// SyncGroup paginates projects + issues under the named team key (ticket
// vocab: "group"). Returns *ErrGroupNotFound if the key isn't visible to
// the API key.
//
// Pagination is cursor-based and serial — Linear's pageInfo.endCursor is
// returned in each response and supplied as `after` in the next request.
// No in-tree analog parallelizes this kind of dependent-cursor walk, so
// serial is the only correct shape.
func (b *Backend) SyncGroup(ctx context.Context, group string) (backends.Snapshot, error) {
	snap := backends.Snapshot{}

	// Linear's Query.team takes id only (not key). Resolve key→id by
	// listing teams and matching client-side. One extra HTTP call per
	// SyncGroup is acceptable; teams lists are tiny.
	groups, err := b.Groups(ctx)
	if err != nil {
		return backends.Snapshot{}, fmt.Errorf("linear: resolve team id for %q: %w", group, err)
	}
	var teamID string
	for _, g := range groups {
		if g.Key == group || g.ID == group {
			teamID = g.ID
			break
		}
	}
	if teamID == "" {
		return backends.Snapshot{}, &backends.Error{
			Transient: false,
			Reason:    backends.ReasonNotFound,
			Cause:     &ErrGroupNotFound{GroupKey: group},
		}
	}

	// Projects — paginated under team(id: $teamID).
	after := ""
	for {
		var resp teamProjectsResponse
		vars := map[string]any{"teamID": teamID}
		if after != "" {
			vars["after"] = after
		}
		if err := b.Client.do(ctx, teamProjectsQuery, vars, &resp); err != nil {
			return backends.Snapshot{}, fmt.Errorf("linear: list projects for team %q: %w", group, err)
		}
		if resp.Team == nil {
			return backends.Snapshot{}, &backends.Error{
				Transient: false,
				Reason:    backends.ReasonNotFound,
				Cause:     &ErrGroupNotFound{GroupKey: group},
			}
		}
		for _, p := range resp.Team.Projects.Nodes {
			snap.Projects = append(snap.Projects, projectNodeToSnapshot(group, p))
		}
		if !resp.Team.Projects.PageInfo.HasNextPage {
			break
		}
		after = resp.Team.Projects.PageInfo.EndCursor
	}

	// Issues — same pagination shape.
	after = ""
	for {
		var resp teamIssuesResponse
		vars := map[string]any{"teamID": teamID}
		if after != "" {
			vars["after"] = after
		}
		if err := b.Client.do(ctx, teamIssuesQuery, vars, &resp); err != nil {
			return backends.Snapshot{}, fmt.Errorf("linear: list issues for team %q: %w", group, err)
		}
		if resp.Team == nil {
			// Consistent with projects path; usually Team would be non-nil
			// here since the projects fetch already validated existence,
			// but cover the race / mid-sync deletion case.
			return backends.Snapshot{}, &backends.Error{
				Transient: false,
				Reason:    backends.ReasonNotFound,
				Cause:     &ErrGroupNotFound{GroupKey: group},
			}
		}
		for _, i := range resp.Team.Issues.Nodes {
			snap.Tickets = append(snap.Tickets, issueNodeToSnapshot(group, i))
		}
		if !resp.Team.Issues.PageInfo.HasNextPage {
			break
		}
		after = resp.Team.Issues.PageInfo.EndCursor
	}

	return snap, nil
}

// projectNodeToSnapshot maps a single Linear project node to a
// ProjectSnapshot, joining label nodes into a comma-separated string and
// computing the archived bit from archivedAt presence. Status is passed
// through verbatim — Linear's workspace-level project state enum string.
//
// Field mapping (mirrors the write path in backend_write_project.go):
//
//	Linear.description (short tagline, ≤255) → Snapshot.Summary
//	Linear.content     (long markdown body)  → Snapshot.Description
//
// Asymmetric mapping is intentional — keeps our Summary/Description split
// aligned with how Linear actually stores the two fields.
func projectNodeToSnapshot(group string, p projectNode) backends.ProjectSnapshot {
	return backends.ProjectSnapshot{
		Ref: backends.RemoteRef{
			ID:         p.ID,
			Identifier: "", // Linear projects have no human key separate from name
			URL:        p.URL,
		},
		GroupKey:    group,
		Name:        p.Name,
		Summary:     p.Description, // Linear tagline → our Summary
		Description: p.Content,     // Linear body → our Description
		Status:      p.State,       // workspace-level enum string verbatim
		Priority:    p.Priority,
		Labels:      joinLabelNames(p.Labels.Nodes),
		Archived:    p.ArchivedAt != nil,
	}
}

// issueNodeToSnapshot maps an issue node to a TicketSnapshot. Status is
// the team-scoped WorkflowState's name, passed through verbatim (e.g.
// "In Review" stays "In Review", not normalized to "in_review").
func issueNodeToSnapshot(group string, i issueNode) backends.TicketSnapshot {
	var projRef backends.RemoteRef
	if i.Project != nil {
		projRef = backends.RemoteRef{ID: i.Project.ID, URL: i.Project.URL}
	}
	return backends.TicketSnapshot{
		Ref: backends.RemoteRef{
			ID:         i.ID,
			Identifier: i.Identifier,
			URL:        i.URL,
		},
		GroupKey:    group,
		ProjectRef:  projRef,
		Name:        i.Title,
		Description: i.Description,
		Status:      i.State.Name, // team-scoped workflow state name
		Priority:    i.Priority,
		Labels:      joinLabelNames(i.Labels.Nodes),
		Archived:    i.ArchivedAt != nil,
	}
}

// joinLabelNames flattens label nodes to a comma-separated name list.
// Lossy on color and hierarchy by design — see backends/backend.go package
// doc on label round-trip semantics.
func joinLabelNames(nodes []labelNode) string {
	if len(nodes) == 0 {
		return ""
	}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	return strings.Join(names, ",")
}
