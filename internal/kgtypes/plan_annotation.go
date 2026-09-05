// SPDX-License-Identifier: Apache-2.0

package kgtypes

// plan_annotation.go declares the metadata contract of a plan_annotation node.
//
// THE KEYS ARE CONSTANTS RATHER THAN LITERALS because three packages read them:
// the pre-write guard that refuses a malformed annotation, the reader that
// composes a section's annotation summaries, and the tree renderer's kind
// census. A metadata key spelled by hand in three places is an informal contract
// with nothing holding it together — one rename and the guard validates a key
// nothing reads.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	// AnnotationKindKey carries which of the three kinds the annotation is.
	AnnotationKindKey = "annotation_kind"

	// AnnotationTierKey carries the severity tier, REQUIRED on a finding and
	// meaningless on the other two kinds.
	AnnotationTierKey = "annotation_tier"

	// AnnotationLaneKey carries the reviewing lane, so a plan's annotations can
	// be read back per reviewer.
	AnnotationLaneKey = "reviewer_lane"

	// AnnotationReplacementKey carries the exact replacement text of a needed
	// change, IN THE METADATA VALUE ITSELF. That is the whole carrier: the
	// pre-write guard requires this key to be non-empty and nothing anywhere
	// reads a replacement out of the node's body.
	//
	// AN EARLIER VERSION OF THIS COMMENT NAMED TWO CARRIERS IN CONSECUTIVE
	// SENTENCES — that the key carries the text, and that the text "lives in the
	// node body" — which is the same two-carriers-one-fact confusion this type's
	// severity fields were fixed for, except here there was only ever one carrier
	// and the second was imaginary. It is corrected rather than reconciled.
	//
	// WHAT WAS TRUE IN IT, and is a different point: an annotation's SUMMARY and
	// its BODY are read at different costs, because a section read returns
	// summaries and a reader fetches a body by id. A long replacement text
	// therefore belongs in the annotation's body prose as well, where a reader
	// who fetched it will find it in context — but that is authoring advice about
	// the annotation, not a statement about where this key's value is stored.
	AnnotationReplacementKey = "replacement_text"
)

// Annotation kinds — the three words the reviewer vocabulary defines.
const (
	AnnotationKindCorrect      = "correct"
	AnnotationKindFinding      = "finding"
	AnnotationKindNeededChange = "needed change"
)

// AnnotationKinds is the valid set, in the order a refusal message lists it and
// the order the tree's kind census renders in.
var AnnotationKinds = []string{
	AnnotationKindCorrect,
	AnnotationKindFinding,
	AnnotationKindNeededChange,
}

// IsAnnotationKind reports whether kind is one of the three.
func IsAnnotationKind(kind string) bool {
	return slices.Contains(AnnotationKinds, kind)
}

// AnnotationEdgeMethod tags the relates-to edge that joins an annotation to its
// section, so a reader can tell that edge apart from the many other relates-to
// edges a node carries WITHOUT hydrating the peer. relates-to is the graph's most
// common edge; the method is what makes this one identifiable at the edge.
const AnnotationEdgeMethod = "plan-annotation"

// annotationEdgeSeverity is the payload the annotation edge's Evidence carries:
// the annotation's kind and, where the kind has one, its tier.
//
// WHY THE SEVERITY RIDES THE EDGE AS WELL AS THE NODE. A reviewer scanning a plan
// asks "which sections have unresolved findings, and how bad" — a question about
// the RELATION, answerable from the section's edges alone. Without it that
// question costs a hydrate of every annotation node on the plan just to read two
// short strings off each. The node keeps both fields; this is a second carrier of
// the same facts at the layer that is cheap to read, not a move.
//
// IT IS A JSON BLOB ON Evidence rather than a new edge field, because Evidence,
// Method, Weight and Confidence are what create_batch, link and traverse already
// accept and surface. Weight and Confidence are numeric and cannot carry a tier
// string, so Evidence is the only one of the four that can hold both facts. This
// is the same carrier the positioned-parts idiom uses for a child's position.
type annotationEdgeSeverity struct {
	Kind string `json:"annotation_kind"`
	Tier string `json:"annotation_tier,omitempty"`
}

// ValidateAnnotationSeverity reports whether a kind and tier form a severity that
// can be written at all: the kind is one of the three, and a finding carries the
// tier that makes it actionable.
//
// IT IS THE ONE RULE BOTH CARRIERS OBEY. The node's own contract check and the
// edge payload below both run it, so an annotation cannot exist in a state where
// its node is acceptable and its edge is not, or the reverse. Before this
// existed, an annotation reaching the graph through an ungated path could carry
// NO kind at all, and the edge payload would then serialize that emptiness
// happily — the attach-time refusal printed the payload {"annotation_kind":""}
// as the value to send, which is a refusal instructing the caller to write a
// severity that says nothing.
func ValidateAnnotationSeverity(kind, tier string) error {
	if !IsAnnotationKind(kind) {
		return fmt.Errorf("annotation kind is %q — it must be one of %s", kind, strings.Join(AnnotationKinds, ", "))
	}
	if kind == AnnotationKindFinding && tier == "" {
		// THE KEY IS NAMED HERE rather than by each caller's own sentence. Callers
		// report this error by wrapping it, so a rule that omitted the key would
		// leave every one of them either unactionable or writing a second copy of
		// the rule to add it back.
		return fmt.Errorf("an annotation of kind %q carries no metadata.%s — a finding with no severity is a concern a reader cannot act on",
			AnnotationKindFinding, AnnotationTierKey)
	}
	return nil
}

// MarshalAnnotationEdgeSeverity renders the edge Evidence payload for an
// annotation of the given kind and tier, REFUSING a severity that says nothing.
//
// IT RETURNS AN ERROR RATHER THAN AN EMPTY STRING, for the reason the section
// builder's position payload does: an empty Evidence is not a degraded severity,
// it is INDISTINGUISHABLE from an edge that carries none, which is the legal
// state of every relates-to edge in the graph. A failure absorbed into "" would
// write an annotation whose severity is silently unreadable at the edge.
//
// AND IT VALIDATES BEFORE IT SERIALIZES, which is the part that was missing. A
// kind-less annotation used to marshal cleanly to {"annotation_kind":""}, so
// every reader downstream saw a well-formed payload asserting nothing and the
// tree rendered it as `annotations: 1 ( 1)`. Refusing here puts the check at the
// one point every write and every attach-time guard already passes through.
func MarshalAnnotationEdgeSeverity(kind, tier string) (string, error) {
	if err := ValidateAnnotationSeverity(kind, tier); err != nil {
		return "", err
	}
	blob, err := json.Marshal(annotationEdgeSeverity{Kind: kind, Tier: tier})
	if err != nil {
		return "", fmt.Errorf("marshal annotation edge severity for kind %q: %w", kind, err)
	}
	return string(blob), nil
}

// ParseAnnotationEdgeSeverity reads an annotation edge's kind and tier off its
// Evidence, reporting whether one was found.
//
// A MISS IS SOFT AND NEVER AN ERROR. An edge with no severity is the normal state
// of the graph's most common edge type, so absent, non-JSON or kind-less Evidence
// is a property of the edge rather than a caller mistake. A kind that is not one
// of the three is returned AS READ rather than dropped: the graph says what it
// says, and a reader that silently normalized it would under-report review state.
func ParseAnnotationEdgeSeverity(evidence string) (kind, tier string, ok bool) {
	if evidence == "" {
		return "", "", false
	}
	var s annotationEdgeSeverity
	if err := json.Unmarshal([]byte(evidence), &s); err != nil {
		return "", "", false
	}
	if s.Kind == "" {
		return "", "", false
	}
	return s.Kind, s.Tier, true
}
