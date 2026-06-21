// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// StableID returns a deterministic 16-character hex ID for a node a recipe run
// is about to emit into a target graph. The ID is a SHA-256 truncation over
// (targetGraph, sourceSlug, kind, identity) so re-running the same recipe on the
// same source graph produces the same IDs, making Force=false idempotent and
// Force=true deterministically identifiable.
//
// The component separator is "\x00" so that adjacent components like
// ("hohpe-eip", "") and ("hohpe", "eip") do not collide. The null byte is ASCII
// 0 which never appears in well-formed UTF-8 content.
//
// The targetGraph argument is the target graph key rendered as "<type>/<name>"
// (e.g. "practice/design-patterns") so the same (sourceSlug, kind, identity)
// triple lands in different IDs for different target graphs.
//
// The returned ID is 16 characters (64 bits). The hashing is byte-identical to
// the former server transformer.StableID so IDs emitted before the client
// migration round-trip unchanged.
func StableID(targetGraph, sourceSlug, kind, identity string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(targetGraph))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(sourceSlug))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(identity))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// translatedFromEvidence is the JSON payload carried on every translated-from
// edge. The schema is intentionally minimal — consumers parse it field-by-field
// so additive fields do not break older readers.
type translatedFromEvidence struct {
	// Source is the opaque source slug (e.g. "hohpe-eip"). Used by Force=true
	// selection to delete exactly the nodes from one source run.
	Source string `json:"source"`
}

// TranslatedFromEdge builds the kgwire.BatchEdge a recipe run emits to stamp
// provenance from a target-graph node (the newly-emitted one) back to the
// source node in the source graph. The edge carries Evidence JSON with the
// source slug so Force=true can filter by source.
//
// Both IDs are full string identifiers (FromIdx/ToIdx are -1). The returned edge
// is appended to Result.Lineage. If marshaling fails (never does in practice;
// all fields are strings) the function returns an edge with Evidence == "" rather
// than panicking.
func TranslatedFromEdge(targetNodeID, sourceRawNodeID, sourceSlug string) kgwire.BatchEdge {
	payload, err := json.Marshal(translatedFromEvidence{Source: sourceSlug})
	if err != nil {
		payload = nil
	}
	return kgwire.BatchEdge{
		FromIdx:  -1,
		ToIdx:    -1,
		FromID:   targetNodeID,
		ToID:     sourceRawNodeID,
		Type:     kgtypes.EdgeTranslatedFrom,
		Method:   "transformer",
		Evidence: string(payload),
	}
}

// SourceFromEvidence extracts the Source field from a translated-from edge's
// Evidence blob. Used by Force=true cleanup to match nodes against the current
// source slug. Returns "" on empty or malformed input.
func SourceFromEvidence(evidence string) string {
	if evidence == "" {
		return ""
	}
	var e translatedFromEvidence
	if err := json.Unmarshal([]byte(evidence), &e); err != nil {
		return ""
	}
	return e.Source
}

// TargetKey renders a TargetSpec as "<type>/<name>" for use as the first
// argument to StableID.
func TargetKey(t TargetSpec) string {
	return fmt.Sprintf("%s/%s", t.GraphType, t.Name)
}
