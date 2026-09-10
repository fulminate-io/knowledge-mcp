// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bound_boundary_test.go — WHAT DecodeResult STILL REFUSES NOW THAT IT REFUSES
// NOTHING BY SIZE.
//
// This file used to hold the two size-boundary rows, one per bounded site. Both
// bounds are gone (see uncapped_result_test.go for the rows that replaced them
// and the mutation that would red them), and what survives here is the gate that
// was never about size — the control proving the removal did not take the whole
// of DecodeResult's validation with it.

// TestDecodeResult_RefusesAContractViolationOnItsOwn makes DecodeResult's
// contract validation LOAD-BEARING, which it was not: every other test that
// exercised a non-conforming payload went through RunMCP, where the
// advertised-schema check refuses the same payload a moment later, so deleting
// the ValidateResultPayload call inside DecodeResult left the package green.
//
// DecodeResult is exported and callable with no provider session and no
// advertised schema in play — the contract test calls it that way, and a replay
// or fixture path would. This payload passes the strict Go decode (walk_complete
// simply defaults to false and no field is unknown), so the contract validation
// is the ONLY thing that can refuse it.
func TestDecodeResult_RefusesAContractViolationOnItsOwn(t *testing.T) {
	_, err := DecodeResult("collect_graph", json.RawMessage(`{"nodes":[],"edges":[]}`))
	require.Error(t, err, "a payload missing the required completeness assertion must be refused by DecodeResult itself")
	assert.Contains(t, err.Error(), "walk_complete")
	assert.Contains(t, err.Error(), "collect_graph")
}
