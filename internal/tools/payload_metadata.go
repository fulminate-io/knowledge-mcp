// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"fmt"
)

// payloadMetadata is one metadata map a mutate payload carries, paired with the
// INDEXED FIELD PATH it was found at — "metadata", "nodes[0].metadata",
// "items[2].metadata" — and with the entry's own target id where the shape has
// one.
//
// The path is not decoration. It is the only thing that tells an author WHICH
// batch entry a refusal is about, so a walk returning bare maps would silently
// downgrade every batch diagnostic to "somewhere in this payload".
type payloadMetadata struct {
	Path     string
	ID       string
	Metadata map[string]string
}

// metadataCarrier is what the batch walk reads off one entry: the metadata map
// and the entry's target id. Each arm's own decoder owns the rest of the shape.
type metadataCarrier struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
}

// payloadMetadataMaps returns every metadata map a mutate payload carries,
// across all FOUR carriers: the top-level metadata (create / update / upsert),
// create_batch's nodes[], update_batch's items[] and bulk_update_metadata's
// updates[].
//
// It reads the VERBATIM payload rather than the decoded mutateArgs struct
// because mutateArgs mirrors the scalar params only — it has no Nodes, Items or
// Updates field — so a struct-field walk would see one carrier in four and
// report the other three as carrying nothing.
//
// The top-level entry carries no ID: the target of a single-node op lives in the
// mutate arguments rather than beside the metadata, and the caller supplies it.
//
// A payload that does not fit the shape yields nothing. Reporting a malformed
// payload is the arm's own decode's job, not this walk's.
func payloadMetadataMaps(raw json.RawMessage) []payloadMetadata {
	var payload struct {
		Metadata map[string]string `json:"metadata"`
		Nodes    []metadataCarrier `json:"nodes"`
		Items    []metadataCarrier `json:"items"`
		Updates  []metadataCarrier `json:"updates"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	var found []payloadMetadata
	if len(payload.Metadata) > 0 {
		found = append(found, payloadMetadata{Path: "metadata", Metadata: payload.Metadata})
	}
	groups := []struct {
		field   string
		entries []metadataCarrier
	}{
		{"nodes", payload.Nodes},
		{"items", payload.Items},
		{"updates", payload.Updates},
	}
	for _, g := range groups {
		for i, entry := range g.entries {
			if len(entry.Metadata) == 0 {
				continue
			}
			found = append(found, payloadMetadata{
				Path:     fmt.Sprintf("%s[%d].metadata", g.field, i),
				ID:       entry.ID,
				Metadata: entry.Metadata,
			})
		}
	}
	return found
}
