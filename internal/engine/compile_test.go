// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCompile_UnknownToolDefaultDeny asserts the default-deny contract for an
// unrecognized tool name: Compile returns (nil, false).
func TestCompile_UnknownToolDefaultDeny(t *testing.T) {
	req, ok := Compile("collect", json.RawMessage(`{}`))
	assert.False(t, ok, "unknown tool falls through to legacy")
	assert.Nil(t, req)

	req, ok = Compile("thoughts", json.RawMessage(`{"operation":"recall"}`))
	assert.False(t, ok, "thoughts is specialized — never compiled")
	assert.Nil(t, req)
}

// TestCompile_CodeGraphDefaultDeny asserts search/query over graph=code fall
// through (HandleSearchCode / HandleAnalyzeNode are specialized).
func TestCompile_CodeGraphDefaultDeny(t *testing.T) {
	req, ok := Compile("search", json.RawMessage(`{"query":"x","graph":"code"}`))
	assert.False(t, ok, "search graph=code is specialized")
	assert.Nil(t, req)

	req, ok = Compile("query", json.RawMessage(`{"id":"foo","graph":"code"}`))
	assert.False(t, ok, "query graph=code is specialized")
	assert.Nil(t, req)
}

// TestBuildTarget covers the GraphSelector mapping: empty selector → nil; a
// populated selector maps each field one-for-one.
func TestBuildTarget(t *testing.T) {
	assert.Nil(t, buildTarget("", "", "", "", "", ""), "all-empty selector → nil target (knowledge default)")

	tgt := buildTarget("code", "knowledge", "", "", "", "main")
	if assert.NotNil(t, tgt) {
		assert.Equal(t, "code", tgt.GetGraph())
		assert.Equal(t, "knowledge", tgt.GetRepo())
		assert.Equal(t, "main", tgt.GetBranch())
	}

	tgt = buildTarget("practice", "", "", "", "go", "")
	if assert.NotNil(t, tgt) {
		assert.Equal(t, "practice", tgt.GetGraph())
		assert.Equal(t, "go", tgt.GetLanguage())
	}
}
