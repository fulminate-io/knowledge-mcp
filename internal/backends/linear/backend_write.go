// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// truncateRunes returns s truncated to at most maxRunes RUNES (not bytes),
// never splitting a multibyte UTF-8 sequence. A sub-cap input is returned
// unchanged. Mirrors the rune-counting idiom at validate.go:48
// (utf8.RuneCountInString) — kept LOCAL to the linear adapter rather than
// importing a shared helper, per the no-cross-boundary-shared-package rule.
// Used to cap Linear's `description` field (≤255 chars) without producing
// invalid UTF-8 when the source summary contains multibyte characters.
func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes])
}

// Compile-time assertion: *Backend satisfies backends.Backend. Placed here
// in backend_write.go (rather than backend.go) because backend.go was
// authored in Phase 3 when only 3 of 9 methods were implemented. If any
// method's signature drifts or a method is removed, the build fails here.
// Mirrors internal/llm/anthropic/anthropic.go (var _ llm.Client = (*Service)(nil)).
var _ backends.Backend = (*Backend)(nil)

// resolvedTeam holds team UUID + workflow state map for a single team. Used
// by methods that need team-scoped resolution (issue creates/updates with
// status or labels; project label updates). It carries NO label map: labels
// are resolved one name at a time against the tracker (see ensureLabels), so
// there is nothing team-wide to hold.
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
}

// resolveTeamByKey fetches a team's UUID + workflow states by team key. Used
// by CreateProject / CreateTicket where the caller supplies the group key
// explicitly. Labels are NOT read here — ensureLabels resolves them per name
// off the ID this returns. Returns *ErrGroupNotFound if Linear's response has
// no matching team — wrapped as *backends.Error{Reason: ReasonNotFound}.
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

// resolveTeamByID fetches a team's workflow states by team UUID. Used by
// UpdateTicket (which derives team UUID via issueByID) and UpdateProject
// (which derives team UUID via projectByID's first team). Labels are NOT read
// here — ensureLabels resolves them per name off the ID this returns.
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

// normalizeTeam flattens the wire-shape teamWithStates into the adapter's
// internal state-name→UUID map.
func normalizeTeam(t *teamWithStates) *resolvedTeam {
	out := &resolvedTeam{
		ID:     t.ID,
		Key:    t.Key,
		States: make(map[string]string, len(t.States.Nodes)),
	}
	for _, s := range t.States.Nodes {
		out.States[s.Name] = s.ID
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

// ensureLabels resolves every label name in `commaList` to a label UUID on
// the team, creating the ones the team genuinely does not have. Returns the
// UUIDs in the caller's DECLARATION ORDER, which is the order they reach
// issueCreate / issueUpdate as input.labelIds.
//
// Each name is resolved by ONE filtered lookup against the tracker
// (teamLabelByNameQuery), keyed on the team UUID this already holds. The
// adapter does no case folding of its own: eqIgnoreCase makes the comparison
// Linear's, which is the only comparison that agrees with the duplicate-name
// rule Linear enforces at create time. Reading the team's labels in bulk was
// the earlier shape and it was wrong — a team can hold more labels than one
// page returns, so a label past the page looked absent, was re-created, and
// Linear rejected the create as a duplicate, losing the whole write.
//
// TWO PASSES, and the order is the contract. Pass one resolves every DISTINCT
// requested name; pass two creates the ones the team lacks. A name the caller
// wrote more than once is ONE label: it is looked up once and created at most
// once, and the definition of "more than once" is the tracker's own (see
// indexOfSameLabel). Creating once per OCCURRENCE would send the tracker a
// create for a name it had just been given, which it rejects as a duplicate —
// stranding the first label with no ticket landed. Resolving and creating
// name-by-name instead would let a list like "brand-new,Bug" create brand-new
// and only then refuse on the ambiguous Bug, leaving a label written on the
// tracker with no ticket landed. Nothing removes it: the locked contract is
// that this adapter does not clean up after itself. R1 refuses BEFORE ANY
// CREATE, and R2 forbids a ticket landing on a partial set; a label written
// ahead of a refusal is a partial write of the same kind.
//
// The four match arms, all evaluated in pass one:
//   - exactly one  → reuse it; no issueLabelCreate is sent.
//   - zero         → remember it for pass two (see the HARD-ERROR note below).
//   - two or more  → REFUSE, naming every match with its scope, with nothing
//     created. Picking one would be a silent guess, and an unwanted label is
//     invisible after the fact.
//   - lookup error → propagate, naming the label, with nothing created. A
//     failed lookup is not an absent label; treating it as one would create a
//     duplicate.
//
// CONTRACT: FIRST line MUST be the empty-list return-fast. Empty-list-with-
// nil-team is the legitimate CreateProject-without-labels path; an
// implementation that derefs `team` before checking commaList would
// nil-panic on this path. It is also what keeps a label-free write at ZERO
// lookup requests.
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

	// PASS ONE — resolve every DISTINCT name. Nothing is written to the tracker
	// in this loop, so any refusal or failure below leaves it untouched.
	// `declared` records which resolved entry each DECLARED occurrence maps to,
	// so a repeated name costs one lookup while still contributing one id per
	// occurrence to the caller's list, which is what the caller asked for.
	resolved := make([]resolvedLabel, 0, len(parts))
	declared := make([]int, 0, len(parts))
	for _, raw := range parts {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if i := indexOfSameLabel(resolved, name); i >= 0 {
			declared = append(declared, i)
			continue
		}
		matches, more, err := b.lookupLabel(ctx, team, name)
		if err != nil {
			return nil, err
		}
		if len(matches) > 1 {
			return nil, &backends.Error{
				Transient: false,
				Reason:    backends.ReasonInvalidArgument,
				Cause:     &ErrAmbiguousLabel{Requested: name, Matches: matches, More: more},
			}
		}
		id := ""
		if len(matches) == 1 {
			id = matches[0].ID
		}
		resolved = append(resolved, resolvedLabel{Name: name, ID: id})
		declared = append(declared, len(resolved)-1)
	}

	// PASS TWO — create the distinct names the team genuinely lacks, in
	// declared order. Reaching here means every name resolved cleanly, so no
	// create here can be stranded by a later refusal on an earlier name.
	for i := range resolved {
		if resolved[i].ID != "" {
			continue
		}
		// Genuinely absent — create it on the team. HARD-ERROR on failure.
		var resp issueLabelCreateResponse
		input := map[string]any{
			"name":   resolved[i].Name,
			"teamId": team.ID,
		}
		if err := b.Client.do(ctx, issueLabelCreateMutation,
			map[string]any{"input": input}, &resp); err != nil {
			return nil, fmt.Errorf("linear: create label %q on team %q: %w", resolved[i].Name, team.ID, err)
		}
		resolved[i].ID = resp.IssueLabelCreate.IssueLabel.ID
	}

	out := make([]string, 0, len(declared))
	for _, i := range declared {
		out = append(out, resolved[i].ID)
	}
	return out, nil
}

// indexOfSameLabel returns the index in resolved of an already-resolved
// request for the same label as name, or -1 when name is new to this list.
//
// SAME MEANS WHAT THE TRACKER MEANS, AS CLOSELY AS THIS SIDE CAN. The lookup
// filters with eqIgnoreCase, so the tracker returns the same row for two
// spellings that differ only in case. Whether it also REJECTS a create of the
// second as a duplicate of the first is the ticket's premise P14, which is
// unverified and cannot be settled read-only, so this fold is a defensive
// approximation of the tracker's rule and not a restatement of it. This
// adapter collapses two such entries into one request, carrying the FIRST
// spelling the caller wrote.
//
// THIS IS NOT THE CLIENT DECIDING WHETHER A LABEL EXISTS. That comparison is
// still the tracker's, and it remains the only thing that decides reuse
// against create. This fold answers a narrower question the tracker is never
// asked: whether two entries in ONE caller's list are the same request.
// Without it "dup,dup" issues two creates, the tracker rejects the second, and
// the write hard-errors with the first label already written and no ticket
// landed — the partial write the two-pass shape exists to prevent, reached by
// a different road.
//
// strings.EqualFold is Unicode simple case folding, an approximation of
// whatever eqIgnoreCase does exactly, which the tracker documents nowhere. The
// approximation is only ever applied WITHIN one caller-supplied list, and for
// the exact-repeat case that prompted it any fold at all collapses the pair.
//
// WHERE THE APPROXIMATION CAN DIVERGE, both directions named so a later reader
// does not have to derive them. strings.EqualFold folds U+017F LONG S to 's',
// which Go's own strings.ToLower does not, and it folds U+212A KELVIN SIGN to
// 'k', which an ASCII-only fold does not (Go's ToLower folds that one too, so
// do not reach for lower() to tell the two comparators apart). On a pair the
// tracker keeps apart this side collapses two labels into one, and the caller
// receives one id where it asked for two. It does NOT fold sharp-s against SS,
// which a full-folding comparator would: on such a pair pass two sends two
// creates and the tracker rejects the second, which is the partial write this
// fold exists to prevent. Neither pair has a live instance and neither is a
// regression (the bulk-map shape under-folded on every non-identical
// spelling). Settling it needs a case-differing label create on the tracker.
//
// A NOTE FOR WHOEVER STRENGTHENS THE FIXTURE: the fold test drives the Kelvin
// pair, which separates EqualFold from an ASCII-only fold. Long s separates it
// from an ASCII fold AND from Go's ToLower, so a fixture built on long s would
// pin one more axis at no cost.
func indexOfSameLabel(resolved []resolvedLabel, name string) int {
	for i := range resolved {
		if strings.EqualFold(resolved[i].Name, name) {
			return i
		}
	}
	return -1
}

// resolvedLabel is one requested name after pass one of ensureLabels. An empty
// ID means the tracker holds no label by that name and pass two must create
// it; a set ID is an existing label to reuse.
type resolvedLabel struct {
	Name string
	ID   string
}

// lookupLabel asks the tracker which labels on this team match `name`,
// returning them in the order Linear returned them plus whether Linear
// reported further matches past the page read. The comparison is the
// tracker's (eqIgnoreCase); the caller's spelling is sent verbatim.
//
// EVERY failure arm — transport, GraphQL errors[], and a 200 whose team is
// null — is an error NAMING THE LABEL. That is deliberate: a lookup that
// failed is not a label that is absent, and the two must never converge,
// because reading a failure as an absence is what sends a duplicate create.
//
// One request per name, issued serially. That is right here: the calls sit on
// a single write path, the list is a handful of names, and the package has no
// parallel primitive — do not add one for this.
func (b *Backend) lookupLabel(ctx context.Context, team *resolvedTeam, name string) ([]LabelMatch, bool, error) {
	var resp teamLabelByNameResponse
	if err := b.Client.do(ctx, teamLabelByNameQuery,
		map[string]any{"id": team.ID, "name": name}, &resp); err != nil {
		// client.do already pre-wraps as *backends.Error.
		return nil, false, fmt.Errorf("linear: look up label %q on team %q: %w", name, team.ID, err)
	}
	if resp.Team == nil {
		return nil, false, &backends.Error{
			Transient: false,
			Reason:    backends.ReasonNotFound,
			Cause: fmt.Errorf("linear: look up label %q: %w", name,
				&ErrGroupNotFound{GroupKey: team.ID}),
		}
	}
	matches := make([]LabelMatch, 0, len(resp.Team.Labels.Nodes))
	for _, n := range resp.Team.Labels.Nodes {
		scope := workspaceLabelScope
		if n.Team != nil {
			scope = "team " + n.Team.Key
		}
		matches = append(matches, LabelMatch{ID: n.ID, Name: n.Name, Scope: scope})
	}
	return matches, resp.Team.Labels.PageInfo.HasNextPage, nil
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
