// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// Compile-time assertion: *Backend satisfies backends.Backend. Placed here
// in backend_write.go (rather than backend.go) because backend.go was
// authored in Phase 3 when only 3 of 9 methods were implemented. If any
// method's signature drifts or a method is removed, the build fails here.
// Mirrors domains/llm/anthropic/anthropic.go (var _ llm.Client = (*Service)(nil)).
var _ backends.Backend = (*Backend)(nil)

// resolvedTeam holds team UUID + workflow state map + label-name→id map
// for a single team. Used by methods that need team-scoped resolution
// (issue creates/updates with status or labels; project label updates).
//
// Result is fresh per call — we don't cache across calls because (a) state
// maps change as Linear admins add/remove states, (b) per-call caching is
// sufficient since each write touches at most one team for resolution, and
// (c) cross-call caching would need invalidation logic that is out of scope
// for T1.
type resolvedTeam struct {
	ID     string
	Key    string            // optional; useful for error messages
	States map[string]string // state name → UUID
	Labels map[string]string // label name → UUID
}

// resolveTeamByKey fetches a team's UUID + workflow states + labels by team
// key. Used by CreateProject / CreateTicket where the caller supplies the
// group key explicitly. Returns *ErrGroupNotFound if Linear's response has
// resp.Team == nil — wrapped as *backends.Error{Reason: ReasonNotFound}.
func (b *Backend) resolveTeamByKey(ctx context.Context, groupKey string) (*resolvedTeam, error) {
	var resp teamByKeyResponse
	if err := b.Client.do(ctx, teamByKeyQuery, map[string]any{"key": groupKey}, &resp); err != nil {
		// client.do already pre-wraps as *backends.Error.
		return nil, fmt.Errorf("linear: lookup team %q: %w", groupKey, err)
	}
	if len(resp.Teams.Nodes) == 0 {
		return nil, &backends.Error{
			Transient: false,
			Reason:    backends.ReasonNotFound,
			Cause:     &ErrGroupNotFound{GroupKey: groupKey},
		}
	}
	return normalizeTeam(&resp.Teams.Nodes[0]), nil
}

// resolveTeamByID fetches a team's workflow states + labels by team UUID.
// Used by UpdateTicket (which derives team UUID via issueByID) and
// UpdateProject (which derives team UUID via projectByID's first team).
// Returns *backends.Error{Reason: ReasonNotFound, Cause: *ErrGroupNotFound}
// if Linear's response has resp.Team == nil.
func (b *Backend) resolveTeamByID(ctx context.Context, teamID string) (*resolvedTeam, error) {
	var resp teamByIDResponse
	if err := b.Client.do(ctx, teamByIDQuery, map[string]any{"id": teamID}, &resp); err != nil {
		return nil, fmt.Errorf("linear: lookup team id %q: %w", teamID, err)
	}
	if resp.Team == nil {
		return nil, &backends.Error{
			Transient: false,
			Reason:    backends.ReasonNotFound,
			Cause:     &ErrGroupNotFound{GroupKey: teamID},
		}
	}
	return normalizeTeam(resp.Team), nil
}

// normalizeTeam flattens the wire-shape teamWithStatesLabels into the
// adapter's internal name→UUID maps.
func normalizeTeam(t *teamWithStatesLabels) *resolvedTeam {
	out := &resolvedTeam{
		ID:     t.ID,
		Key:    t.Key,
		States: make(map[string]string, len(t.States.Nodes)),
		Labels: make(map[string]string, len(t.Labels.Nodes)),
	}
	for _, s := range t.States.Nodes {
		out.States[s.Name] = s.ID
	}
	for _, l := range t.Labels.Nodes {
		out.Labels[l.Name] = l.ID
	}
	return out
}

// resolveStatus returns the UUID for the named workflow state on this team,
// or *ErrUnknownState if the team doesn't have a state by that name. Empty
// status string returns the empty string (caller drops the field from the
// mutation input — backend assigns its default state). Used ONLY for issue
// state resolution; project status uses the workspace-level enum verbatim.
func (b *Backend) resolveStatus(team *resolvedTeam, status, groupKey string) (string, error) {
	if status == "" {
		return "", nil
	}
	if team == nil {
		return "", &backends.Error{
			Transient: false,
			Reason:    backends.ReasonInvalidArgument,
			Cause:     fmt.Errorf("linear: resolveStatus with nil team: %w", ErrInvalidArgument),
		}
	}
	id, ok := team.States[status]
	if !ok {
		available := make([]string, 0, len(team.States))
		for name := range team.States {
			available = append(available, name)
		}
		sort.Strings(available)
		return "", &backends.Error{
			Transient: false,
			Reason:    backends.ReasonUnknownState,
			Cause:     &ErrUnknownState{GroupKey: groupKey, State: status, Available: available},
		}
	}
	return id, nil
}

// ensureLabels ensures every label name in `commaList` exists on the team,
// creating any missing ones via issueLabelCreate. Returns the slice of
// label UUIDs in declaration order.
//
// CONTRACT: FIRST line MUST be the empty-list return-fast. Empty-list-with-
// nil-team is the legitimate CreateProject-without-labels path; an
// implementation that derefs `team` before checking commaList would
// nil-panic on this path.
//
// Defensive secondary check: non-empty list with nil team returns wrapped
// ErrInvalidArgument — programmer-error path, but wrapped rather than
// panicked.
//
// Per the user-locked answer to the label-create-on-the-fly question:
// HARD-ERROR if issueLabelCreate fails for any reason (including
// duplicate-name race); T1 is an adapter, not a concurrency layer.
func (b *Backend) ensureLabels(ctx context.Context, team *resolvedTeam, commaList string) ([]string, error) {
	if commaList == "" {
		return nil, nil
	}
	if team == nil {
		return nil, &backends.Error{
			Transient: false,
			Reason:    backends.ReasonInvalidArgument,
			Cause:     fmt.Errorf("linear: ensureLabels with nil team and labels %q: %w", commaList, ErrInvalidArgument),
		}
	}
	parts := strings.Split(commaList, ",")
	out := make([]string, 0, len(parts))
	for _, raw := range parts {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if id, ok := team.Labels[name]; ok {
			out = append(out, id)
			continue
		}
		// Missing — create it on the team. HARD-ERROR on failure.
		var resp issueLabelCreateResponse
		input := map[string]any{
			"name":   name,
			"teamId": team.ID,
		}
		if err := b.Client.do(ctx, issueLabelCreateMutation,
			map[string]any{"input": input}, &resp); err != nil {
			return nil, fmt.Errorf("linear: create label %q on team %q: %w", name, team.ID, err)
		}
		newID := resp.IssueLabelCreate.IssueLabel.ID
		team.Labels[name] = newID
		out = append(out, newID)
	}
	return out, nil
}

// isInvalidEnumError reports whether err looks like a Linear GraphQL
// "invalid enum value" rejection for the named state field. Used by
// UpdateProject to disambiguate workspace-level Project.state misses
// (which become *ErrUnknownState) from auth/transport/5xx errors
// (which propagate verbatim via %w-wrap).
//
// Linear's GraphQL error message shape for invalid enum values is
// "Variable '$input' got invalid value \"<state>\"; Expected type
// 'ProjectStatusType'." or similar — we match the user's state string
// appearing in the error message AND a structural marker like "invalid
// value" or "Expected type". Deliberately conservative: false negatives
// (rare-form Linear errors that should classify as unknown-state but
// don't) propagate via %w as a generic linear-adapter error, which the
// caller can still read; false positives would silently swallow real
// transport errors and are FORBIDDEN.
func isInvalidEnumError(err error, state string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnauthorized) {
		return false
	}
	msg := err.Error()
	if state == "" || !strings.Contains(msg, state) {
		return false
	}
	// Structural marker — at least one must be present.
	return strings.Contains(msg, "invalid value") || strings.Contains(msg, "Expected type")
}
