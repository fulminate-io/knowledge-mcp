// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// StableID returns a deterministic 16-character hex ID for a node a recipe run
// is about to emit into a target graph. The ID is a SHA-256 truncation over
// (targetGraph, sourceSlug, kind, identity) so re-running the same recipe on the
// same source graph produces the same IDs, which is what makes a re-run
// idempotent and what lets the landing's collision read recognize a resident row
// by id.
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

// versionIdentitySeparator joins a resident id to the version integer inside the
// twin's identity component. It is the SAME null byte StableID already uses
// between its four components, for the same reason: it never appears in
// well-formed UTF-8, so ("abc", 12) and ("abc1", 2) cannot collide.
const versionIdentitySeparator = "\x00v"

// VersionedTwinID mints the id of version n of a resident node.
//
// THE TWIN MUST CARRY A DIFFERENT ID FROM THE RESIDENT, and that is a constraint
// rather than a preference: the landing writes through create_batch, which is an
// ADD and not an upsert, so a twin written under the resident's id would
// overwrite whatever a human had since edited into that row. That overwrite is
// exactly what the versioned-twin ruling exists to prevent, so the id is derived
// instead.
//
// IT DERIVES FROM THE RESIDENT'S ID, not from the emit's identity string. The
// identity feeds StableID and is never stored (assembleEmittedNode consumes it),
// so at collision time the only thing the landing holds about the resident is its
// id — which is itself a StableID over the identity, so nothing is lost.
//
// IT IS DETERMINISTIC, which is what keeps a landing idempotent in the same way
// the base id already is: re-running against an unchanged target reproduces the
// same twin id at the same version, and a third run that finds v2 resident mints
// v3 rather than a fresh node per run. A random or timestamped suffix would make
// every re-run land a new node and defeat the collision branch entirely.
func VersionedTwinID(targetGraph, sourceSlug, kind, residentID string, version int) string {
	return StableID(targetGraph, sourceSlug, kind,
		residentID+versionIdentitySeparator+strconv.Itoa(version))
}

// TargetKey renders a TargetSpec as "<type>/<name>" for use as the first
// argument to StableID.
func TargetKey(t TargetSpec) string {
	return fmt.Sprintf("%s/%s", t.GraphType, t.Name)
}

// containsEvidence is the JSON payload a contains edge carries. Only the
// position is read here, and the schema is parsed field-by-field so additive
// fields do not break older readers.
type containsEvidence struct {
	// Position is the child's index under its parent, stamped as a string by
	// both raw collectors.
	Position string `json:"position"`
}

// positionFromEvidence extracts a child's position from a contains edge's
// Evidence blob, reporting whether one was found.
//
// It NEVER returns an error. An absent, malformed or non-integer position is a
// property of the source graph, not a mistake by the recipe author, so it is a
// soft miss reported as ok=false — the same treatment an orphan edge already
// gets when a neighbor field is collected.
func positionFromEvidence(evidence string) (int, bool) {
	if evidence == "" {
		return 0, false
	}
	var e containsEvidence
	if err := json.Unmarshal([]byte(evidence), &e); err != nil {
		return 0, false
	}
	pos, err := strconv.Atoi(e.Position)
	if err != nil {
		return 0, false
	}
	return pos, true
}
