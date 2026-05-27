// SPDX-License-Identifier: Apache-2.0

// Package backends defines the abstraction for external project/ticket
// trackers (Linear, Jira, GitHub Issues, etc.) that knowledge optionally
// round-trips project + ticket containers to.
//
// # Activation model
//
// Each adapter sub-package self-reports activation via an Enabled() bool that
// reads a backend-specific key — the linear adapter, for example, reads
// config.LinearAPIKey() (the [credentials].linear_api_key field in
// ~/.knowledge/config, falling back to the LINEAR_API_KEY env var). The
// provider in this package (Available, Default, ByName) is a
// closed switch — adding a new backend means editing the switch in
// provider.go, NOT calling any Register() function. There is intentionally no
// init-time registry: backends are a closed set of operator-facing
// integrations, not an extension point for third-party plugins.
//
// # Mapping
//
// knowledge.Project ↔ remote project-equivalent.
// knowledge.Ticket  ↔ remote issue-equivalent.
// Each adapter defines its own internal mapping inside its sub-package.
//
// # Status
//
// Status is an opaque string. The interface MUST NOT enumerate or translate
// states — adapters pass the backend's state name through verbatim. T1 makes
// no attempt to normalize "in-progress" vs "In Progress" vs "In progress";
// callers store and supply whatever the backend returns.
//
// # Labels
//
// Labels round-trip by name only. Color, parent label, hierarchy are lossy.
// Adapters are responsible for creating missing labels under the relevant
// group on push.
package backends

import "context"

// Backend is the contract every project/ticket adapter must satisfy.
//
// Method semantics:
//   - Name returns a stable, lowercase identifier (e.g. "linear"). The same
//     string is stored on local nodes as the `backend` metadata key by T2 and
//     used by ByName() to route per-node updates by T3.
//   - Groups lists every group (Linear group, Jira project, GitHub repo) the
//     credentials can reach. Used by T2 for create-time group resolution.
//   - SyncGroup pulls the full state of one group into a Snapshot — both
//     projects and tickets, including archived items. Used by T4.
//   - CreateProject / CreateTicket return a populated RemoteRef on success.
//   - UpdateProject / UpdateTicket apply a partial diff; nil-pointer fields in
//     the Diff are left unchanged.
//   - ArchiveProject / ArchiveTicket archive the remote item; never delete
//     hard. Re-archiving an already-archived item is a no-op (or wrapped
//     error per the adapter's choice — Linear treats it as idempotent).
//
// All methods return wrapped errors on transport / API failure; the wrapping
// must include enough context to identify the operation and the failing
// resource (group key, ref ID, status name on push, etc.).
type Backend interface {
	Name() string
	Groups(ctx context.Context) ([]Group, error)
	SyncGroup(ctx context.Context, group string) (Snapshot, error)

	CreateProject(ctx context.Context, args ProjectCreateArgs) (RemoteRef, error)
	UpdateProject(ctx context.Context, ref RemoteRef, diff ProjectDiff) error
	ArchiveProject(ctx context.Context, ref RemoteRef) error

	CreateTicket(ctx context.Context, args TicketCreateArgs) (RemoteRef, error)
	UpdateTicket(ctx context.Context, ref RemoteRef, diff TicketDiff) error
	ArchiveTicket(ctx context.Context, ref RemoteRef) error
}
