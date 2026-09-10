// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_docs_describe_test.go — the GUIDE'S describe section, checked against
// the CHECKED-IN SCHEMA rather than against a second copy of the prose.
//
// WHY THE SCHEMA IS THE COMPARISON. The guide pastes an example declaration, as
// it pastes the other two contract schemas; a page and a paste can drift, and
// the way that drift shows up is a collector author writing a provider the
// client refuses for a reason the page never mentioned. Reading the required
// keys off the artifact the comparator actually walks is what makes the page's
// omission of one a failure here rather than a support question later.

// TestGuide_DocumentsEveryRequiredKeyOfTheDescribeContract is the one-source
// pin: every top-level key the checked-in describe schema REQUIRES is named on
// the page a collector author reads.
func TestGuide_DocumentsEveryRequiredKeyOfTheDescribeContract(t *testing.T) {
	page := guidePage(t)

	var schema struct {
		Required []string `json:"required"`
	}
	require.NoError(t, json.Unmarshal(externalcollector.DescribeContractJSON(), &schema))
	require.NotEmpty(t, schema.Required, "the checked-in describe schema requires nothing; this row would pass vacuously")

	for _, key := range schema.Required {
		assert.Contains(t, page, key,
			"the guide's describe section does not mention the required key %q — an author reading the page would write a provider the client refuses", key)
	}
}

// TestGuide_SaysTheDescribeToolIsRequiredAndNamesIt pins the three facts an
// author needs before they can act: that there is a second tool, what it is
// called, and that a provider without it is refused.
func TestGuide_SaysTheDescribeToolIsRequiredAndNamesIt(t *testing.T) {
	page := guidePage(t)
	for _, needed := range []string{
		externalcollector.DescribeToolName,
		"contract/collector_describe.schema.json",
		"--describe-env-table",
		"--describe-tool",
		"--describe-env-sensitive",
	} {
		assert.Contains(t, page, needed, "the guide must name %q", needed)
	}
	assert.Contains(t, page, "not-carried",
		"the guide must document the fourth environment disposition, or an author reads three classes and declares a name under the wrong one")
	assert.Contains(t, page, "all_node_types",
		"the guide must document the family-level selector alongside the node-type list it is not")
}

// TestGuide_SaysTheTwoLLMAxesStayTheOperators is the spend rule on the page. A
// collector author reading that their suggestion is applied would declare one
// expecting it to take effect.
func TestGuide_SaysTheTwoLLMAxesStayTheOperators(t *testing.T) {
	page := guidePage(t)
	assert.True(t,
		strings.Contains(page, "SUGGESTION and never a setting") || strings.Contains(page, "printed by the add rather than applied"),
		"the guide must say the two LLM axes are a suggestion the operator's flags decide")
	assert.Contains(t, page, "--summarize",
		"and must name the flag that turns one on, since that is the operator's next move")
}

// guidePage reads the custom-collector guide THROUGH this module's docs symlink,
// which is what puts it in this module's test-cache key. See
// collect_docs_contract_test.go for why the symlink exists and what a checkout
// without one costs.
//
// IT NAMES ITS PAGE RATHER THAN TAKING ONE. Every caller passed the same literal,
// which is a parameter that decides nothing and which the lint matrix reports as
// such. A second guide page gets the parameter back in the change that adds its
// first reader, where the two spellings can be seen together.
const guidePageName = "custom_collector.md"

func guidePage(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(docsRoot(t), "tools", guidePageName))
	require.NoError(t, err)
	return string(raw)
}
