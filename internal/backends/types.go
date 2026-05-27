// SPDX-License-Identifier: Apache-2.0

package backends

// RemoteRef identifies a remote-side object after a successful create. Adapters
// populate fields when the backend supplies them; absent fields are empty
// strings. State carries the backend-assigned state name verbatim (Linear
// project workspace-level enum like "started"; Linear issue team-scoped
// workflow name like "In Review") so callers can record the initial status
// without a separate read-back round-trip.
type RemoteRef struct {
	ID         string // backend-internal UUID (e.g. Linear issue UUID)
	Identifier string // human-readable identifier (e.g. "ABC-42")
	URL        string // deeplink to the backend's UI
	State      string // verbatim backend state name at the time of create
}

// Group is a backend-agnostic scope: a Linear group, a Jira project, a GitHub
// repo, etc. Adapters define their own internal mapping to whatever the
// backend calls this concept.
type Group struct {
	ID   string // backend's internal group UUID
	Key  string // human-readable key (e.g. "ABC")
	Name string // display name
}

// ProjectCreateArgs carries the fields needed to create a project on a backend.
// Status is opaque — adapters pass the string through to the backend verbatim.
// Labels is a comma-separated list of label NAMES (lossy on color/hierarchy).
//
// Summary vs Description: backends typically distinguish a short tagline from
// a long markdown body. Linear's ProjectCreateInput.description caps at 255
// chars (it's the tagline) and the long body lives in `content`. We map
// Summary → backend tagline, Description → backend body. Tickets don't need
// this split — Linear's IssueCreateInput.description IS the markdown body.
type ProjectCreateArgs struct {
	GroupKey    string // human-readable group identifier (e.g. "ABC"); adapter resolves to UUID
	Name        string
	Summary     string // short tagline; adapters map to whatever short field the backend has (Linear: description, ≤255)
	Description string // long markdown body; adapters map to backend's body field (Linear: content)
	Status      string // verbatim backend state name; empty = backend default
	Priority    int    // backend-specific scale (Linear: 0-4); 0 = no priority
	Labels      string // comma-separated label names; missing labels created on the fly
}

// TicketCreateArgs carries the fields needed to create a ticket. ProjectRef is
// the RemoteRef of the parent project (from a prior CreateProject); empty for
// ungrouped tickets.
type TicketCreateArgs struct {
	GroupKey    string
	ProjectRef  RemoteRef // parent project remote ref; zero value = no parent
	Name        string
	Description string
	Status      string
	Priority    int
	Labels      string
}

// ProjectDiff is the partial-update payload for UpdateProject. Each field is a
// pointer so the adapter can distinguish "not provided" (nil) from "clear to
// empty string" (non-nil, empty). Status passthrough applies as in Create.
// Summary vs Description: see ProjectCreateArgs godoc — Summary is the short
// tagline, Description is the long body.
type ProjectDiff struct {
	Name        *string
	Summary     *string
	Description *string
	Status      *string
	Priority    *int
	Labels      *string
}

// TicketDiff mirrors ProjectDiff for tickets.
type TicketDiff struct {
	Name        *string
	Description *string
	Status      *string
	Priority    *int
	Labels      *string
}

// ProjectSnapshot is the read-side view of one remote project. Summary vs
// Description mirrors ProjectCreateArgs (see godoc there): Summary is the
// short tagline, Description is the long markdown body.
type ProjectSnapshot struct {
	Ref         RemoteRef
	GroupKey    string
	Name        string
	Summary     string // short tagline (Linear: description)
	Description string // long markdown body (Linear: content)
	Status      string // verbatim
	Priority    int
	Labels      string
	Archived    bool
}

// TicketSnapshot is the read-side view of one remote ticket.
type TicketSnapshot struct {
	Ref         RemoteRef
	GroupKey    string
	ProjectRef  RemoteRef // zero value if ticket has no parent project
	Name        string
	Description string
	Status      string
	Priority    int
	Labels      string
	Archived    bool
}

// Snapshot is the full result of SyncGroup: every project and ticket the
// backend reports under the named group, including archived items.
type Snapshot struct {
	Projects []ProjectSnapshot
	Tickets  []TicketSnapshot
}
