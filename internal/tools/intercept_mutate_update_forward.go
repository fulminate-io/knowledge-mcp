// SPDX-License-Identifier: Apache-2.0

// intercept_mutate_update_forward.go — the payload the local-only update path
// forwards after a successful tracker write.
//
// It is a sibling of intercept_mutate.go rather than part of it because that file
// sits against the repo's per-file length ceiling, and the dispatch head is where
// a reader looks for ROUTING, not for a wire shape one arm re-encodes. Behaviour
// is unchanged: the same two declarations, moved verbatim.

package tools

import "encoding/json"

// marshalForwardedMutateUpdateArgs builds a fresh JSON payload for the
// forwarded local-only mutate(update). Strips backend-private metadata
// keys from a copy of a.Metadata so the caller's struct is untouched
// (caller-arg-safety for retry idempotency). Typed (vs map[string]any)
// so errchkjson is satisfied.
func marshalForwardedMutateUpdateArgs(a mutateArgs, backendName string) json.RawMessage {
	payload := forwardedMutateUpdatePayload{
		Operation:   "update",
		ID:          a.ID,
		Name:        a.Name,
		Description: a.Description,
		Summary:     a.Summary,
		Content:     a.Content,
		Status:      a.Status,
		Keywords:    a.Keywords,
		// Top-level source is correct HERE even though the per-type router strips
		// it for findings (whose source lives in metadata): a backend-backed node
		// is a tracker-backed work item, never a finding.
		Source:   a.Source,
		Metadata: stripBackendPrivateMetadata(a.Metadata, backendName),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// Cannot fail: typed struct of strings + a string-string map.
		// Defensive return for errchkjson.
		return json.RawMessage("{}")
	}
	return b
}

// forwardedMutateUpdatePayload is the typed wire shape forwarded to the local
// update path after a successful Linear update.
type forwardedMutateUpdatePayload struct {
	Operation   string            `json:"operation"`
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Content     string            `json:"content,omitempty"`
	Status      string            `json:"status,omitempty"`
	Keywords    string            `json:"keywords,omitempty"`
	Source      string            `json:"source,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}
