// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// CreateProject — group-key-driven team resolution; project status is
// passed verbatim (workspace-level enum). Labels resolve against the
// supplied group's team. Linear requires teamIds on project create, so
// we always resolve the team (regardless of whether labels were
// supplied).
func (b *Backend) CreateProject(ctx context.Context, args backends.ProjectCreateArgs) (backends.RemoteRef, error) {
	team, err := b.resolveTeamByKey(ctx, args.GroupKey)
	if err != nil {
		return backends.RemoteRef{}, err
	}
	labelIDs, err := b.ensureLabels(ctx, team, args.Labels)
	if err != nil {
		return backends.RemoteRef{}, err
	}
	// Linear distinguishes the short tagline (`description`, ≤255 chars)
	// from the long markdown body (`content`, no length cap). We map
	// args.Summary → description and args.Description → content. Sending
	// our long Description into Linear's `description` is exactly what the
	// 255-char "Argument Validation Error" was rejecting before this fix.
	// Both fields are optional on Linear's side, so omit when empty.
	input := map[string]any{
		"teamIds":  []string{team.ID},
		"name":     args.Name,
		"priority": args.Priority,
	}
	if args.Summary != "" {
		input["description"] = args.Summary
	}
	if args.Description != "" {
		input["content"] = args.Description
	}
	if args.Status != "" {
		input["state"] = args.Status // workspace-level enum string verbatim
	}
	if len(labelIDs) > 0 {
		input["labelIds"] = labelIDs
	}
	var resp projectCreateResponse
	if err := b.Client.do(ctx, projectCreateMutation,
		map[string]any{"input": input}, &resp); err != nil {
		return backends.RemoteRef{}, fmt.Errorf("linear: create project in team %q: %w", args.GroupKey, err)
	}
	return backends.RemoteRef{
		ID:    resp.ProjectCreate.Project.ID,
		URL:   resp.ProjectCreate.Project.URL,
		State: resp.ProjectCreate.Project.State,
	}, nil
}

// UpdateProject — interface stays as locked: no groupKey parameter.
// Status is the workspace-level enum string sent verbatim (no team
// resolution). Labels require team resolution; we look up the project's
// first team via projectByID. Multi-team caveat: projects can span
// multiple teams in Linear; T1 picks the first team Linear returns from
// projectByID for label resolution.
//
// Error rewrap discipline: arbitrary errors are NOT rewrapped as
// *ErrUnknownState. The pre-rewrap predicate isInvalidEnumError inspects
// the underlying error and only matches Linear's invalid-enum signature;
// auth (ErrUnauthorized), transport, 5xx, ctx-cancel, and unrelated
// GraphQL errors propagate verbatim through %w.
func (b *Backend) UpdateProject(ctx context.Context, ref backends.RemoteRef, diff backends.ProjectDiff) error {
	input := map[string]any{}
	if diff.Name != nil {
		input["name"] = *diff.Name
	}
	// Same Summary→description / Description→content split as CreateProject.
	if diff.Summary != nil {
		input["description"] = *diff.Summary
	}
	if diff.Description != nil {
		input["content"] = *diff.Description
	}
	if diff.Priority != nil {
		input["priority"] = *diff.Priority
	}
	if diff.Status != nil {
		input["state"] = *diff.Status // workspace-level enum, verbatim
	}
	if diff.Labels != nil {
		labelIDs, err := b.resolveProjectLabels(ctx, ref, *diff.Labels)
		if err != nil {
			return err
		}
		input["labelIds"] = labelIDs
	}
	if len(input) == 0 {
		return nil // empty diff: no-op
	}
	var resp projectUpdateResponse
	if err := b.Client.do(ctx, projectUpdateMutation,
		map[string]any{"id": ref.ID, "input": input}, &resp); err != nil {
		// Only re-wrap as *ErrUnknownState when the error genuinely looks
		// like Linear's invalid-enum rejection for the user's state string.
		// Auth, transport, 5xx, ctx-cancel, and unrelated GraphQL errors
		// propagate verbatim through %w.
		if diff.Status != nil && isInvalidEnumError(err, *diff.Status) {
			return &backends.Error{
				Transient: false,
				Reason:    backends.ReasonUnknownState,
				Cause:     &ErrUnknownState{GroupKey: "", State: *diff.Status},
			}
		}
		return fmt.Errorf("linear: update project %q: %w", ref.ID, err)
	}
	return nil
}

// resolveProjectLabels looks up the project's first team and resolves
// the comma-separated label list against it (creating any missing
// labels). Extracted from UpdateProject to keep the method body short
// and the multi-team caveat localized.
func (b *Backend) resolveProjectLabels(ctx context.Context, ref backends.RemoteRef, labels string) ([]string, error) {
	var pr projectByIDResponse
	if err := b.Client.do(ctx, projectByIDQuery,
		map[string]any{"id": ref.ID}, &pr); err != nil {
		return nil, fmt.Errorf("linear: lookup project %q: %w", ref.ID, err)
	}
	if pr.Project == nil || len(pr.Project.Teams.Nodes) == 0 {
		return nil, &backends.Error{
			Transient: false,
			Reason:    backends.ReasonNotFound,
			Cause:     &ErrGroupNotFound{GroupKey: "<project " + ref.ID + " has no teams>"},
		}
	}
	firstTeam := pr.Project.Teams.Nodes[0]
	team, err := b.resolveTeamByID(ctx, firstTeam.ID)
	if err != nil {
		return nil, err
	}
	return b.ensureLabels(ctx, team, labels)
}

// ArchiveProject — bare projectArchive(id) call. Per locked answer to the
// re-archive question: trust Linear's idempotent archive behavior.
func (b *Backend) ArchiveProject(ctx context.Context, ref backends.RemoteRef) error {
	var resp projectArchiveResponse
	if err := b.Client.do(ctx, projectArchiveMutation,
		map[string]any{"id": ref.ID}, &resp); err != nil {
		return fmt.Errorf("linear: archive project %q: %w", ref.ID, err)
	}
	return nil
}
