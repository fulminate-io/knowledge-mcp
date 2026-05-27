// SPDX-License-Identifier: Apache-2.0

package treesitter

import "testing"

// TestClassifyTestKindHCL verifies the filename-driven dispatch.
func TestClassifyTestKindHCL(t *testing.T) {
	cases := []struct {
		desc     string
		path     string
		wantTest bool
		wantKind TestKind
	}{
		{".tftest.hcl → test", "main.tftest.hcl", true, TestKindTest},
		{".tftest.hcl deep path → test", "modules/network/main.tftest.hcl", true, TestKindTest},
		{".tf regular → none", "main.tf", false, TestKindNone},
		{".tfvars → none", "terraform.tfvars", false, TestKindNone},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			gotTest, gotKind := classifyTestKindHCL(nil, nil, "block", "", ChunkContext{}, tc.path)
			if gotTest != tc.wantTest {
				t.Errorf("IsTest = %v, want %v", gotTest, tc.wantTest)
			}
			if gotKind != tc.wantKind {
				t.Errorf("TestKind = %q, want %q", gotKind, tc.wantKind)
			}
		})
	}
}

// TestExtendFrameworksHCL verifies the extender appends FrameworkHCLTfTest
// when the file ends in `.tftest.hcl` and leaves the slice unchanged
// otherwise. Mirrors TestExtendFrameworksRust at chunker_rust_test.go:234.
func TestExtendFrameworksHCL(t *testing.T) {
	cases := []struct {
		desc string
		path string
		want []Framework
	}{
		{"tftest.hcl appends", "main.tftest.hcl", []Framework{FrameworkHCLTfTest}},
		{"deep tftest.hcl appends", "modules/net/main.tftest.hcl", []Framework{FrameworkHCLTfTest}},
		{"tf regular unchanged", "main.tf", nil},
		{"tfvars unchanged", "terraform.tfvars", nil},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := extendFrameworksHCL(nil, nil, tc.path, nil)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i, fw := range got {
				if fw != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, fw, tc.want[i])
				}
			}
		})
	}
}
