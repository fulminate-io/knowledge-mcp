// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// CreateTicket — group-key-driven team resolution; status resolves against
// team workflow; each label resolves through its own filtered lookup on the
// team's label connection (see ensureLabels).
func (b *Backend) CreateTicket(ctx context.Context, args backends.TicketCreateArgs) (backends.RemoteRef, error) {
	team, err := b.resolveTeamByKey(ctx, args.GroupKey)
	if err != nil {
		return backends.RemoteRef{}, err
	}
	stateID, err := b.resolveStatus(team, args.Status, args.GroupKey)
	if err != nil {
		return backends.RemoteRef{}, err
	}
	labelIDs, err := b.ensureLabels(ctx, team, args.Labels)
	if err != nil {
		return backends.RemoteRef{}, err
	}
	input := map[string]any{
		"teamId":      team.ID,
		"title":       args.Name,
		"description": args.Description,
		"priority":    args.Priority,
	}
	if stateID != "" {
		input["stateId"] = stateID
	}
	if len(labelIDs) > 0 {
		input["labelIds"] = labelIDs
	}
	if args.ProjectRef.ID != "" {
		input["projectId"] = args.ProjectRef.ID
	}
	var resp issueCreateResponse
	if err := b.Client.do(ctx, issueCreateMutation,
		map[string]any{"input": input}, &resp); err != nil {
		return backends.RemoteRef{}, fmt.Errorf("linear: create ticket in team %q: %w", args.GroupKey, err)
	}
	return backends.RemoteRef{
		ID:         resp.IssueCreate.Issue.ID,
		Identifier: resp.IssueCreate.Issue.Identifier,
		URL:        resp.IssueCreate.Issue.URL,
		State:      resp.IssueCreate.Issue.State.Name,
	}, nil
}

// UpdateTicket — interface stays as locked: no groupKey parameter.
// Issue.team is a non-null singular field in Linear; we resolve via
// issueByID to get the team UUID, then use teamByID for workflow states.
// Labels resolve per name off that team UUID (see ensureLabels).
func (b *Backend) UpdateTicket(ctx context.Context, ref backends.RemoteRef, diff backends.TicketDiff) error {
	team, err := b.lookupTicketTeam(ctx, ref, diff)
	if err != nil {
		return err
	}
	input := map[string]any{}
	if diff.Name != nil {
		input["title"] = *diff.Name
	}
	if diff.Description != nil {
		input["description"] = *diff.Description
	}
	if diff.Priority != nil {
		input["priority"] = *diff.Priority
	}
	if diff.Status != nil {
		stateID, err := b.resolveStatus(team, *diff.Status, team.Key)
		if err != nil {
			return err
		}
		if stateID != "" {
			input["stateId"] = stateID
		}
	}
	if diff.Labels != nil {
		labelIDs, err := b.ensureLabels(ctx, team, *diff.Labels)
		if err != nil {
			return err
		}
		input["labelIds"] = labelIDs
	}
	if len(input) == 0 {
		return nil
	}
	var resp issueUpdateResponse
	if err := b.Client.do(ctx, issueUpdateMutation,
		map[string]any{"id": ref.ID, "input": input}, &resp); err != nil {
		return fmt.Errorf("linear: update ticket %q: %w", ref.ID, err)
	}
	return nil
}

// lookupTicketTeam fetches the resolved team for an UpdateTicket call,
// or returns nil if the diff doesn't require team-scoped resolution
// (only Status/Labels changes need it). Extracted to keep UpdateTicket's
// body short.
func (b *Backend) lookupTicketTeam(ctx context.Context, ref backends.RemoteRef, diff backends.TicketDiff) (*resolvedTeam, error) {
	if diff.Status == nil && diff.Labels == nil {
		return nil, nil
	}
	var ir issueByIDResponse
	if err := b.Client.do(ctx, issueByIDQuery,
		map[string]any{"id": ref.ID}, &ir); err != nil {
		return nil, fmt.Errorf("linear: lookup ticket %q: %w", ref.ID, err)
	}
	if ir.Issue == nil {
		return nil, &backends.Error{
			Transient: false,
			Reason:    backends.ReasonNotFound,
			Cause:     fmt.Errorf("linear: ticket %q not found", ref.ID),
		}
	}
	return b.resolveTeamByID(ctx, ir.Issue.Team.ID)
}

// ArchiveTicket — bare issueArchive(id) call. Per locked answer to the
// re-archive question: trust Linear's idempotent archive behavior.
func (b *Backend) ArchiveTicket(ctx context.Context, ref backends.RemoteRef) error {
	var resp issueArchiveResponse
	if err := b.Client.do(ctx, issueArchiveMutation,
		map[string]any{"id": ref.ID}, &resp); err != nil {
		return fmt.Errorf("linear: archive ticket %q: %w", ref.ID, err)
	}
	return nil
}
