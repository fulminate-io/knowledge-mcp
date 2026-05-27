// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

func TestIsBashTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"foo.bats", true},
		{"tests/auth.bats", true},
		// T3-B: bats only — generic shell heuristics rejected.
		{"test_foo.sh", false},
		{"test_foo.bash", false},
		{"foo.sh", false},
		{"production.sh", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := isBashTestFile(tc.path); got != tc.want {
				t.Errorf("isBashTestFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestExtMapBats(t *testing.T) {
	cases := []struct {
		path string
		want Language
	}{
		{"foo.bats", LangBash},
		{"foo.sh", LangBash},
		{"foo.bash", LangBash},
		{"foo.zsh", LangBash},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := DetectLanguage(tc.path); got != tc.want {
				t.Errorf("DetectLanguage(%q) = %q; want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestClassifyTestKindBash exercises the Bucket A degraded-path classifier:
// any declaration chunk in a `.bats` file is TestKindTest, with setup /
// teardown function names mapped to setup/teardown.
func TestClassifyTestKindBash(t *testing.T) {
	cases := []struct {
		desc     string
		path     string
		src      string
		name     string
		wantTest bool
		wantKind TestKind
	}{
		{
			desc:     "bats_setup_function",
			path:     "auth.bats",
			src:      `setup() { echo "x"; }`,
			name:     "setup",
			wantTest: true,
			wantKind: TestKindSetup,
		},
		{
			desc:     "bats_teardown_function",
			path:     "auth.bats",
			src:      `teardown() { echo "x"; }`,
			name:     "teardown",
			wantTest: true,
			wantKind: TestKindTeardown,
		},
		{
			desc:     "bats_setup_file",
			path:     "auth.bats",
			src:      `setup_file() { echo "x"; }`,
			name:     "setup_file",
			wantTest: true,
			wantKind: TestKindSetup,
		},
		{
			desc:     "bats_teardown_file",
			path:     "auth.bats",
			src:      `teardown_file() { echo "x"; }`,
			name:     "teardown_file",
			wantTest: true,
			wantKind: TestKindTeardown,
		},
		{
			desc:     "bats_helper_function",
			path:     "auth.bats",
			src:      `helper() { echo "x"; }`,
			name:     "helper",
			wantTest: true,
			wantKind: TestKindTest,
		},
		{
			desc:     "non_bats_file_drops",
			path:     "production.sh",
			src:      `setup() { echo "x"; }`,
			name:     "setup",
			wantTest: false,
			wantKind: TestKindNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			var found *Chunk
			for i := range res.Chunks {
				if res.Chunks[i].Name == tc.name {
					found = &res.Chunks[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("chunk %q not found in %+v", tc.name, res.Chunks)
			}
			if found.IsTest != tc.wantTest {
				t.Errorf("IsTest=%v; want %v", found.IsTest, tc.wantTest)
			}
			if found.TestKind != tc.wantKind {
				t.Errorf("TestKind=%q; want %q", found.TestKind, tc.wantKind)
			}
		})
	}
}
