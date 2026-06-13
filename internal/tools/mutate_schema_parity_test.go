// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusLockdownComment is the verbatim two-line block asserting the status
// field is an OPEN STRING, not a closed enum. It appears in kgtypes (client)
// and store/graph_types_vocab.go (server). The client copy is guarded here; the
// server copy by a sibling assertion in the server module.
const statusLockdownComment = "// Status values for work nodes. Internal vocabulary, NOT a closed enum —\n" +
	"// see mutateRequestArgs.Status comment for the open-string contract."

// TestStatusOpenStringLockdown_Untouched proves the per-type-param update
// rejection did NOT close the status enum: the status schema property carries no
// Enum (rejection is param-PRESENCE-per-type, never status-VALUE validation),
// and the client-side open-string lockdown comment block is present verbatim. A
// future edit that adds a status Enum or deletes the lockdown comment fails here.
func TestStatusOpenStringLockdown_Untouched(t *testing.T) {
	status, ok := mutateProperties()["status"]
	require.True(t, ok, "status must be a declared mutate param")
	assert.Empty(t, status.Enum, "status must stay an OPEN STRING — no closed-enum validation")

	src, err := os.ReadFile("../kgtypes/graph_types.go")
	require.NoError(t, err)
	assert.Contains(t, string(src), statusLockdownComment,
		"the kgtypes status open-string lockdown comment must be present verbatim")
}

// updateParamClass classifies how a mutateProperties() schema key is handled on
// the mutate(update) path. The buckets align with the Phase-2 unified seam.
type updateParamClass int

const (
	// classRoutedUniversal — routed for every claimed update type: the universal
	// passthrough scalars (top-level set_fields / set_metadata / target selectors).
	classRoutedUniversal updateParamClass = iota
	// classRoutedPerType — routed into metadata for the owning node type only
	// (criterion: command/criterion_type; rule: scope/enforcement; finding:
	// evidence/source) by handleClientMutateUpdateTyped.
	classRoutedPerType
	// classRejected — declared in the schema but NOT routable on the update path
	// (a create/link/answer/thought/charge/batch param). A typed update carrying
	// one for the wrong type is rejected loudly; otherwise it is a no-op the
	// update path does not consume.
	classRejected
)

// updateParamClassification is the EXHAUSTIVE classification of every
// mutateProperties() key for the update operation. It is the schema↔handler
// parity contract: the test below cross-checks it against the LIVE schema in
// BOTH directions so a newly-added schema param with no classification FAILS
// until someone routes or rejects it, and a stale map key (removed from the
// schema) FAILS as drift. Driving the test off mutateProperties() directly
// (not a hand-copied key list) is what makes a silently-dropped param impossible.
var updateParamClassification = map[string]updateParamClass{
	// Universal passthrough (top-level set_fields / set_metadata / target).
	"operation":             classRoutedUniversal,
	"id":                    classRoutedUniversal,
	"ids":                   classRoutedUniversal,
	"name":                  classRoutedUniversal,
	"description":           classRoutedUniversal,
	"summary":               classRoutedUniversal,
	"content":               classRoutedUniversal,
	"status":                classRoutedUniversal,
	"metadata":              classRoutedUniversal,
	"format":                classRoutedUniversal,
	"graph":                 classRoutedUniversal,
	"language":              classRoutedUniversal,
	"expand_to_descendants": classRoutedUniversal,
	"keywords":              classRoutedUniversal,
	// source routes universally too: finding → metadata (per-type), every other
	// type → the node Source field via the Phase-1 widening. Classified per-type
	// because the finding arm routes it into metadata.
	"source": classRoutedPerType,
	// Per-type (routed into metadata by the typed update router).
	"command":        classRoutedPerType,
	"criterion_type": classRoutedPerType,
	"scope":          classRoutedPerType,
	"enforcement":    classRoutedPerType,
	"evidence":       classRoutedPerType,
	// Rejected on the update path (create/link/answer/thought/charge/batch params).
	"type":            classRejected,
	"question_id":     classRejected,
	"concludes":       classRejected,
	"step_id":         classRejected,
	"from":            classRejected,
	"to":              classRejected,
	"relationship":    classRejected,
	"conclusion":      classRejected,
	"findings":        classRejected,
	"binary_vector":   classRejected,
	"confidence":      classRejected,
	"method":          classRejected,
	"edge_evidence":   classRejected,
	"last_validated":  classRejected,
	"link_graph":      classRejected,
	"branches_from":   classRejected,
	"links":           classRejected,
	"session":         classRejected,
	"ticket_id":       classRejected,
	"polarity":        classRejected,
	"weight":          classRejected,
	"reasoning":       classRejected,
	"charge_evidence": classRejected,
	"thought_parent":  classRejected,
	"references":      classRejected,
	"items":           classRejected,
	"nodes":           classRejected,
	"edges":           classRejected,
	"updates":         classRejected,
}

// TestMutateSchemaUpdateParityClassification proves schema↔handler completeness
// for the update path: every mutateProperties() key is classified (routed or
// rejected), and the classification map has no entry absent from the live
// schema. This is the greenfield regression backstop — a new mutate param that
// nobody routes or rejects fails here instead of silently falling on the floor.
func TestMutateSchemaUpdateParityClassification(t *testing.T) {
	schema := mutateProperties()
	require.NotEmpty(t, schema, "mutateProperties() must declare params")

	// Direction 1: every LIVE schema key must be classified.
	for key := range schema {
		_, ok := updateParamClassification[key]
		assert.Truef(t, ok,
			"schema param %q is unclassified for the update path — route it (universal/per-type) or reject it in updateParamClassification", key)
	}

	// Direction 2: every classification entry must exist in the LIVE schema
	// (drift guard — a removed/renamed schema key must not linger here).
	for key := range updateParamClassification {
		_, ok := schema[key]
		assert.Truef(t, ok,
			"classification map names %q which is absent from mutateProperties() — stale entry (schema drift)", key)
	}
}
