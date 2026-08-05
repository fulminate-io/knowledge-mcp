// SPDX-License-Identifier: Apache-2.0

// deny_export_test.go — pins the exported IsDeniedLanguage predicate against a
// known-positive-and-negative pair so a constant-true (or constant-false) stub
// cannot pass: yaml is denied, go is supported.

package ast

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

func TestIsDeniedLanguage_Exported(t *testing.T) {
	if !IsDeniedLanguage(treesitter.LangYaml) {
		t.Errorf("IsDeniedLanguage(yaml) = false; yaml is on the deny list, want true")
	}
	if IsDeniedLanguage(treesitter.LangGo) {
		t.Errorf("IsDeniedLanguage(go) = true; go is supported, want false")
	}
}
