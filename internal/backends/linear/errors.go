// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnauthorized is returned when api.linear.app responds 401/403 — the
// LINEAR_API_KEY is missing, malformed, or revoked. Callers (T2/T3) surface
// this to the user so they can rotate the key.
var ErrUnauthorized = errors.New("linear: unauthorized — check LINEAR_API_KEY")

// ErrInvalidArgument is returned for programmer errors at adapter boundaries
// — e.g. ensureLabels invoked with a nil team and a non-empty label list,
// which is impossible by contract but worth catching defensively rather
// than panicking on a nil-deref. Wrapped via fmt.Errorf("...: %w",
// ErrInvalidArgument) so callers can errors.Is-distinguish from network
// or auth errors.
var ErrInvalidArgument = errors.New("linear: invalid argument")

// ErrUnknownState wraps a request that referenced a workflow state name not
// present on the target team (issue path) OR a workspace-level Project
// state enum value Linear rejected (project path). The two paths are
// distinguished by GroupKey:
//   - issue path: GroupKey is the team key (e.g. "ABC"), State is the name
//   - project path: GroupKey is empty, State is the rejected enum string
//
// Available is the list of valid state names on the target team (issue
// path). Empty when the project path triggered or the caller didn't have
// access to the team's resolved state map at error-construction time. The
// renderer surfaces it as "available: A, B, C" so the caller doesn't have
// to call list_issue_statuses just to find a typo.
type ErrUnknownState struct {
	GroupKey  string
	State     string
	Available []string
}

func (e *ErrUnknownState) Error() string {
	if e.GroupKey == "" {
		return fmt.Sprintf("linear: unknown project state %q (workspace-level enum)", e.State)
	}
	base := fmt.Sprintf("linear: unknown workflow state %q on team %q", e.State, e.GroupKey)
	if len(e.Available) > 0 {
		return base + " — available: " + strings.Join(e.Available, ", ")
	}
	return base
}

// ErrGroupNotFound wraps a request that referenced a team/group key absent
// from the workspace's visible teams.
type ErrGroupNotFound struct {
	GroupKey string
}

func (e *ErrGroupNotFound) Error() string {
	return fmt.Sprintf("linear: group %q not found in workspace", e.GroupKey)
}

// workspaceLabelScope is the scope name an ambiguity refusal prints for a
// label Linear returns with team: null — a workspace-scoped label, visible to
// every team, which is why it can collide with a team-scoped label of the
// same name.
const workspaceLabelScope = "workspace"

// LabelMatch is one label a filtered lookup matched, carrying the scope it
// lives at so a refusal can distinguish two matches that share a name.
// Scope is "team <key>" for a team-scoped label and workspaceLabelScope for
// a workspace-scoped one.
type LabelMatch struct {
	ID    string
	Name  string
	Scope string
}

// ErrAmbiguousLabel wraps a requested label name the tracker matched more
// than once — a team-scoped and a workspace-scoped label of the same name, or
// two case variants of it. Choosing one would be a silent guess at which
// label the caller meant, and attaching the wrong label is invisible after
// the fact, so the adapter refuses before any create and names every match it
// read.
//
// More reports that the tracker said further matches exist beyond the page
// the lookup read; without it a truncated list would read as a complete one.
type ErrAmbiguousLabel struct {
	Requested string
	Matches   []LabelMatch
	More      bool
}

func (e *ErrAmbiguousLabel) Error() string {
	parts := make([]string, 0, len(e.Matches))
	for _, m := range e.Matches {
		parts = append(parts, fmt.Sprintf("%q (%s, id %s)", m.Name, m.Scope, m.ID))
	}
	msg := fmt.Sprintf("linear: label %q is ambiguous — it matches %d labels: %s",
		e.Requested, len(e.Matches), strings.Join(parts, "; "))
	if e.More {
		msg += "; further matches exist beyond the ones listed"
	}
	return msg + ". Rename or remove one of them, or ask for the label by an unambiguous name"
}

// graphQLError is the shape of a single error in a Linear GraphQL response's
// top-level `errors` array. Used internally by client.do to summarize
// errors. Not exported — callers see wrapped fmt.Errorf strings or the typed
// sentinels above.
type graphQLError struct {
	Message    string         `json:"message"`
	Path       []string       `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// formatGraphQLError renders a graphQLError into a one-line debugging string
// that includes Linear's actual rejection reason. Linear's GraphQL server
// reports "Argument Validation Error" as the bare Message and stuffs the
// real cause into Extensions (notably extensions.userPresentableMessage,
// extensions.code, extensions.field). Surfacing only Message hides every
// useful signal — the original do() loss of this info is what made
// validation errors un-debuggable.
func formatGraphQLError(e graphQLError) string {
	var b strings.Builder
	b.WriteString(e.Message)
	if upm, ok := e.Extensions["userPresentableMessage"].(string); ok && upm != "" && upm != e.Message {
		b.WriteString(" — ")
		b.WriteString(upm)
	}
	if code, ok := e.Extensions["code"].(string); ok && code != "" {
		fmt.Fprintf(&b, " [code=%s]", code)
	}
	if field, ok := e.Extensions["field"].(string); ok && field != "" {
		fmt.Fprintf(&b, " [field=%s]", field)
	}
	if t, ok := e.Extensions["type"].(string); ok && t != "" {
		fmt.Fprintf(&b, " [type=%s]", t)
	}
	if len(e.Path) > 0 {
		fmt.Fprintf(&b, " [path=%s]", strings.Join(e.Path, "."))
	}
	return b.String()
}
