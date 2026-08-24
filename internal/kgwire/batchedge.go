// SPDX-License-Identifier: Apache-2.0

// Package kgwire holds client-only wire build-carriers that never cross the
// wire as themselves — they are the LLM-facing struct shapes the client
// assembles before converting to the generated proto (gen/knowledge/v1) for
// transport. It is a neutral low-level client leaf importing ONLY
// gen/knowledge/v1, pkg/kgtypes, and stdlib — NEVER pkg/store — so the ~40
// build sites across collector/projects/tools/postpopulate share one carrier
// without dragging the storage engine into the client or risking an import
// cycle through engine/ or the collector subtree.
package kgwire

import (
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// BatchEdge describes an edge using array indices into the nodes slice passed
// to a batch create. The server resolves indices to generated node IDs.
// If FromIdx or ToIdx is -1, the corresponding FromID/ToID string is used
// instead (for linking to nodes that already exist outside the batch).
//
// This is the client build-carrier mirroring the locked ABI of
// pkg/store.BatchEdge (pkg/store/db_types.go) field-for-field. It carries no
// proto value (so it is copylocks-safe) and converts to the wire form via
// ToProto before transport.
type BatchEdge struct {
	FromIdx    int              // index into nodes slice (-1 to use FromID)
	ToIdx      int              // index into nodes slice (-1 to use ToID)
	FromID     string           // used when FromIdx is -1
	ToID       string           // used when ToIdx is -1
	Type       kgtypes.EdgeType // edge type
	Weight     float64          // optional edge weight (for weighted analyzers)
	Confidence float64          // optional 0.0–1.0 confidence score (e.g. log correlation strength)
	Method     string           // optional: how the edge was discovered (e.g. "cloud-collect")
	Evidence   string           // optional: supporting evidence (e.g. JSON-serialized metadata)
	// LastValidated is the optional timestamp the edge was last verified,
	// carried AS-GIVEN onto the resolved edge. Zero time = unset.
	LastValidated time.Time
	// ContributionHash is the client-computed per-row digest of
	// docs/collect-contribution-hash.md section D, stamped by the collect path
	// and stored by the server AS-IS.
	//
	// IT RIDES THE CARRIER RATHER THAN A PARALLEL ARRAY, unlike the node digests,
	// and that asymmetry is deliberate: edges are re-ordered and re-sliced by
	// BatchEdgesToProto and the byte-split chunker, so a digest traveling
	// alongside them by index would need bookkeeping at every one of those hops.
	// On the struct it survives all of them with none. The zero value means
	// unstamped — every non-collect build site leaves it so.
	ContributionHash [32]byte
}

// ToProto converts the client build-carrier into the generated wire form.
// Type is cast to the proto's open-vocabulary string field; LastValidated
// rides as int64 unix-nanos (zero time → 0 = unset), preserving sub-second
// precision. Mirrors the collector/remote sink's batchEdgesToProto.
//
// An UNSTAMPED ContributionHash goes on the wire as nil rather than 32 zero
// bytes, so "no digest supplied" stays distinguishable from a digest whose
// value happens to be zero.
func (e BatchEdge) ToProto() *knowledgev1.BatchEdge {
	return &knowledgev1.BatchEdge{
		ContributionHash: contributionHashBytes(e.ContributionHash),
		FromIdx:          int32(e.FromIdx),
		ToIdx:            int32(e.ToIdx),
		FromId:           e.FromID,
		ToId:             e.ToID,
		Type:             string(e.Type),
		Weight:           e.Weight,
		Confidence:       e.Confidence,
		Method:           e.Method,
		Evidence:         e.Evidence,
		LastValidated:    lastValidatedNanos(e.LastValidated),
	}
}

// BatchEdgesToProto converts a slice of build-carriers into the generated wire
// form. Empty input → nil.
func BatchEdgesToProto(edges []BatchEdge) []*knowledgev1.BatchEdge {
	if len(edges) == 0 {
		return nil
	}
	out := make([]*knowledgev1.BatchEdge, len(edges))
	for i, e := range edges {
		out[i] = e.ToProto()
	}
	return out
}

// contributionHashBytes encodes the per-row digest onto the wire, mapping the
// zero array to nil (unset) so an unstamped edge is distinguishable from one
// carrying an all-zero digest.
func contributionHashBytes(h [32]byte) []byte {
	if h == ([32]byte{}) {
		return nil
	}
	out := make([]byte, len(h))
	copy(out, h[:])
	return out
}

// lastValidatedNanos encodes a time.Time onto the int64 unix-nanos wire form,
// mapping the zero time.Time to 0 (unset).
func lastValidatedNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
