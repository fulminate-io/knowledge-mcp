// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// context_validate_test.go — the LOUDEST EARLY SITE for a context declaration
// this client cannot satisfy.
//
// THE ENTRY VALIDATOR IS WHERE AN OPERATOR FINDS OUT. It runs on every load and
// again inside `knowledge collector add` before the entry is written, so a
// mistyped family name is refused while the operator is still looking at the
// command that produced it. The collect path validates again, because a
// hand-edited file passes through no write path — but by then the entry is
// installed and the refusal reads as a broken collect rather than a bad entry.

// declaringEntry builds a well-formed http entry carrying a context declaration.
func declaringEntry(decl externalcollector.ContextDeclaration) Entry {
	return Entry{
		Type:    TransportHTTP,
		URL:     "https://collector.example.invalid/mcp",
		Tool:    "collect",
		Context: decl,
	}
}

// TestValidateEntry_RefusesAnUnsupplyableContextDeclaration is the early-site
// half of the same refusal the collect path performs.
func TestValidateEntry_RefusesAnUnsupplyableContextDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		decl    externalcollector.ContextDeclaration
		wantHas string
	}{
		// THE DECLARATION IS COHERENT ON EVERY OTHER AXIS. With node_fields and no
		// node_types it would be refused as incoherent instead — a refusal whose
		// message also contains "cloud", so the row would pass with the retirement
		// check removed. Measured, not supposed: inverting that check left this row
		// green until the declaration was made valid everywhere else.
		{"a retired graph family", externalcollector.ContextDeclaration{
			"cloud": {
				NodeTypes:  []string{"cloud-resource"},
				NodeFields: []string{externalcollector.ContextNodeFieldID},
			},
		}, "retired"},
		{"a node field it cannot produce", externalcollector.ContextDeclaration{
			"gcp": {
				NodeTypes: []string{"gcp-resource"}, NodeFields: []string{"summary"},
			},
		}, `"summary"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEntry("/tmp/collectors.json", "acme", declaringEntry(tc.decl))
			require.Error(t, err, "an unsupplyable declaration is refused before the entry is written")
			assert.Contains(t, err.Error(), tc.wantHas, "the refusal names the offending value")
			assert.Contains(t, err.Error(), "/tmp/collectors.json", "and the file")
			assert.Contains(t, err.Error(), `"acme"`, "and the collector, as every refusal here does")
		})
	}

	// THE SAME-RUN POSITIVE CONTROL, same validator and same entry shape: a
	// satisfiable declaration is admitted, so the refusals above are about the
	// declaration and not about an entry the validator rejects for another reason.
	t.Run("control: a satisfiable declaration is admitted", func(t *testing.T) {
		assert.NoError(t, ValidateEntry("/tmp/collectors.json", "acme", declaringEntry(
			externalcollector.ContextDeclaration{"gcp": {
				NodeTypes:  []string{"gcp-resource"},
				NodeFields: []string{externalcollector.ContextNodeFieldID},
			}})),
			"a family name this loader cannot check against a registry is admitted HERE and "+
				"checked at the fill path, where the registry is readable")
	})

	// AND AN ENTRY DECLARING NOTHING is untouched by any of this, which is what
	// keeps every pre-existing entry loading unchanged.
	t.Run("control: an entry declaring nothing is admitted", func(t *testing.T) {
		assert.NoError(t, ValidateEntry("/tmp/collectors.json", "acme", declaringEntry(nil)))
	})
}
