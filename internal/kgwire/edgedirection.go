// SPDX-License-Identifier: Apache-2.0

package kgwire

// EdgeDirection selects which adjacency a traversal walks for a pivot node.
// This is the 3-value client enum mirroring store.EdgeDirection
// (cmd/knowledge-server/internal/store/edge_iterator.go:56): it supersedes the
// existing 2-value client-local
// copy in the postpopulate package, which folds onto kgwire in a later fan-out
// phase.
type EdgeDirection int

const (
	// OutgoingEdges walks edges where the pivot node is the source.
	OutgoingEdges EdgeDirection = iota
	// IncomingEdges walks edges where the pivot node is the target.
	IncomingEdges
	// BothEdges walks outgoing then incoming.
	BothEdges
)

// String returns a human-readable direction label for logs and debug output.
func (d EdgeDirection) String() string {
	switch d {
	case OutgoingEdges:
		return "outgoing"
	case IncomingEdges:
		return "incoming"
	case BothEdges:
		return "both"
	default:
		return "unknown"
	}
}
