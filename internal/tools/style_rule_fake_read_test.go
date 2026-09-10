// SPDX-License-Identifier: Apache-2.0

package tools

// style_rule_fake_read_test.go holds the helpers every style-rule test shares
// and none of them owns: the ids[] carrier a READ plan selects on, which both
// Execute doubles must model identically, and the rule-list document builder the
// boundary and transition tests compose their fixtures with.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// styleRuleListJSON renders a rule-list document from a Go value.
//
// IT FAILS THE TEST ON A MARSHAL ERROR rather than discarding it. A fixture that
// silently marshaled to nothing would drive the parser with an empty document
// and every refusal assertion would pass for the wrong reason.
func styleRuleListJSON(t *testing.T, doc any) string {
	t.Helper()
	b, err := json.Marshal(doc)
	require.NoError(t, err, "the fixture document must marshal")
	return string(b)
}

// styleFakeReadIDs is the id set a READ plan selects on.
//
// IT READS QueryPlan.by_id AND QueryPlan.ids, AND NEVER Selection.ids. The two
// ids[] carriers have the same name and different jobs: the proto declares
// QueryPlan.ids (field 14) as the read bulk-hydrate carrier and Selection.ids
// (field 7) as the WRITE target set, and the server's newQForPlan picks its
// constructor from by_id, then QueryPlan.ids, then Selection.from_id, falling
// through to a match-all browse. A read plan that puts its ids on the write
// carrier therefore selects NOTHING and pages the whole corpus, without erroring.
//
// A DOUBLE THAT SELECTED ON THE WRITE CARRIER WOULD BLESS THAT PAYLOAD, which is
// how the import's existence read shipped spelled the wrong way: the payload and
// the double agreed with each other and disagreed with the server. The bootstrap
// leg TestExecute_ByIDsReadHonoursTheReadCarrierOnly is the same statement with
// the real engine on the far side.
func styleFakeReadIDs(plan *knowledgev1.QueryPlan) []string {
	ids := plan.GetIds()
	if byID := plan.GetById(); byID != "" {
		ids = append(append([]string{}, ids...), byID)
	}
	return ids
}
