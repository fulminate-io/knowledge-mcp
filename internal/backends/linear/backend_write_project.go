// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// linearDescriptionMaxRunes is the rune cap Linear enforces on a project's
// `description` (the short tagline) — exceeding it triggers an "Argument
// Validation Error". The schema fact is also documented at backend.go:150.
const linearDescriptionMaxRunes = 255

// contentSeparator joins the (overflow) full summary to the markdown body
// when both are present in Linear's uncapped `content` field.
const contentSeparator = "\n\n"

// projectDescriptionField returns the value for Linear's `description`
// (the short tagline) derived from the knowledge summary, rune-capped to
// linearDescriptionMaxRunes. The bool is false when summary is empty, so
// the caller omits the field entirely (empty-omission contract).
func projectDescriptionField(summary string) (string, bool) {
	if summary == "" {
		return "", false
	}
	return truncateRunes(summary, linearDescriptionMaxRunes), true
}

// projectContentField returns the value for Linear's uncapped `content`
// (the markdown body). When the summary overflows the 255-rune description
// cap, the FULL summary is prepended so nothing is lost; the markdown body
// is appended only when non-empty (no trailing separator on an empty body).
// When the summary fits the cap, content is exactly the body. The bool is
// false when neither piece is present, so the caller omits the field.
func projectContentField(summary, body string) (string, bool) {
	overflow := utf8.RuneCountInString(summary) > linearDescriptionMaxRunes
	if !overflow {
		if body == "" {
			return "", false
		}
		return body, true
	}
	content := summary
	if body != "" {
		content += contentSeparator + body
	}
	return content, true
}

// CreateProject — group-key-driven team resolution; project status is
// passed verbatim (workspace-level enum). Each label resolves through its
// own filtered lookup on the supplied group's team (see ensureLabels).
// Linear requires teamIds on project create, so we always resolve the team
// (regardless of whether labels were supplied).
func (b *Backend) CreateProject(ctx context.Context, args backends.ProjectCreateArgs) (backends.RemoteRef, error) {
	team, err := b.resolveTeamByKey(ctx, args.GroupKey)
	if err != nil {
		return backends.RemoteRef{}, err
	}
	labelIDs, err := b.ensureLabels(ctx, team, args.Labels)
	if err != nil {
		return backends.RemoteRef{}, err
	}
	// Linear distinguishes the short tagline (`description`, ≤255 RUNES)
	// from the long markdown body (`content`, no length cap). We map
	// args.Summary → description (rune-capped to 255 via truncateRunes) and
	// args.Description → content. On overflow (summary > 255 runes) the FULL
	// summary is prepended to content so nothing is lost — see
	// projectDescriptionContent. Both fields are optional on Linear's side,
	// so omit when empty.
	input := map[string]any{
		"teamIds":  []string{team.ID},
		"name":     args.Name,
		"priority": args.Priority,
	}
	if desc, ok := projectDescriptionField(args.Summary); ok {
		input["description"] = desc
	}
	if content, ok := projectContentField(args.Summary, args.Description); ok {
		input["content"] = content
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
	// Same rune-capped Summary→description / lossless-overflow→content split
	// as CreateProject (see projectDescriptionField / projectContentField).
	// Only build the fields the diff actually carries: a nil diff.Summary
	// means "don't touch description"; the overflow-into-content rule fires
	// only when diff.Summary is present and exceeds the 255-rune cap.
	if diff.Summary != nil {
		input["description"] = truncateRunes(*diff.Summary, linearDescriptionMaxRunes)
		body := ""
		if diff.Description != nil {
			body = *diff.Description
		}
		if content, ok := projectContentField(*diff.Summary, body); ok {
			input["content"] = content
		}
	} else if diff.Description != nil {
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

// resolveProjectLabels looks up the project's first team and resolves the
// comma-separated label list against it, one filtered lookup per name
// (creating only the names the team genuinely lacks). Extracted from
// UpdateProject to keep the method body short and the multi-team caveat
// localized.
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
