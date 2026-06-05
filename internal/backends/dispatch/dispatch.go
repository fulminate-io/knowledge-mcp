// SPDX-License-Identifier: Apache-2.0

// Package dispatch is the client-side backend write-through dispatcher
// consumed by the cmd/knowledge intercept layer (InterceptMutate).
//
// # Architecture
//
// The async Runner is gone. Linear writes run INLINE from cmd/knowledge
// intercepts BEFORE the local mutate hits the server. There is no
// dirty-flag bookkeeping, no transient-vs-terminal classification, no
// soft-fallback — any backend failure propagates straight to the LLM
// caller.
//
// This package owns only the per-call backend route: take an
// already-fetched *knowledgev1.Node, build the appropriate ProjectDiff /
// TicketDiff, pick UpdateProject vs UpdateTicket vs ArchiveProject vs
// ArchiveTicket, and surface the backend's error verbatim.
package dispatch

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// UpdateArgs carries the partial-update payload routed through the backend.
// Pointer fields distinguish "not provided" (nil) from "clear to empty
// string" (non-nil, empty), matching backends.ProjectDiff/TicketDiff.
type UpdateArgs struct {
	NodeID      string
	Name        *string
	Description *string
	Status      *string
	Priority    *int
	Labels      *string
}

// DeleteArgs carries archive context.
type DeleteArgs struct {
	NodeID string
}

// Update routes a partial-update through the resolved backend. node is
// the caller's already-fetched node (the intercept layer owns lookup);
// backendName is the literal `backend` metadata value on that node;
// backend is the resolved adapter. Returns the backend's error verbatim
// — no transient/terminal classification, no dirty-flag bookkeeping.
//
// Skips silently (returns nil) when the node type is neither project nor
// ticket — plans/phases/steps live only in the knowledge graph.
func Update(
	ctx context.Context,
	node *knowledgev1.Node,
	backendName string,
	backend backends.Backend,
	args UpdateArgs,
) error {
	switch kgtypes.NodeType(node.GetType()) {
	case kgtypes.NodeProject, kgtypes.NodeTicket:
	default:
		return nil
	}
	ref := refFromNode(node, backendName)
	switch kgtypes.NodeType(node.GetType()) {
	case kgtypes.NodeProject:
		return backend.UpdateProject(ctx, ref, projectDiffFromArgs(args))
	case kgtypes.NodeTicket:
		return backend.UpdateTicket(ctx, ref, ticketDiffFromArgs(args))
	}
	return nil
}

// Archive routes an archive through the resolved backend. Same shape as
// Update — caller-owned node lookup, backend's error verbatim.
func Archive(
	ctx context.Context,
	node *knowledgev1.Node,
	backendName string,
	backend backends.Backend,
	_ DeleteArgs,
) error {
	switch kgtypes.NodeType(node.GetType()) {
	case kgtypes.NodeProject, kgtypes.NodeTicket:
	default:
		return nil
	}
	ref := refFromNode(node, backendName)
	switch kgtypes.NodeType(node.GetType()) {
	case kgtypes.NodeProject:
		return backend.ArchiveProject(ctx, ref)
	case kgtypes.NodeTicket:
		return backend.ArchiveTicket(ctx, ref)
	}
	return nil
}

// refFromNode reconstructs the RemoteRef stored on the node at create
// time. Only ID + URL are populated; Identifier and State are NOT
// round-tripped on update/archive.
func refFromNode(node *knowledgev1.Node, backendName string) backends.RemoteRef {
	return backends.RemoteRef{
		ID:  kgtypes.Value(node, backendName+"_id"),
		URL: kgtypes.Value(node, "external_url"),
	}
}

// projectDiffFromArgs translates UpdateArgs into a partial ProjectDiff.
// Pointer-typed fields stay nil when args field is nil so the adapter
// leaves them alone.
func projectDiffFromArgs(args UpdateArgs) backends.ProjectDiff {
	return backends.ProjectDiff{
		Name:        args.Name,
		Description: args.Description,
		Status:      args.Status,
		Priority:    args.Priority,
		Labels:      args.Labels,
	}
}

// ticketDiffFromArgs is the ticket-shaped twin of projectDiffFromArgs.
func ticketDiffFromArgs(args UpdateArgs) backends.TicketDiff {
	return backends.TicketDiff{
		Name:        args.Name,
		Description: args.Description,
		Status:      args.Status,
		Priority:    args.Priority,
		Labels:      args.Labels,
	}
}
