// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// TestRustAttributeHeadName covers the head-name parser independent of the
// AST. The head name drives allowlist dispatch.
func TestRustAttributeHeadName(t *testing.T) {
	cases := []struct {
		attr string
		want string
	}{
		{"#[test]", "test"},
		{"#[tokio::test]", "tokio::test"},
		{"#[rstest]", "rstest"},
		{"#[test_case]", "test_case"},
		{"#[bench]", "bench"},
		{"#[divan::bench]", "divan::bench"},
		{"#[criterion::benchmark]", "criterion::benchmark"},
		{"#[cfg(test)]", "cfg(test)"},
		{"#[cfg(fuzzing)]", "cfg(fuzzing)"},
		{"#[cfg(test, foo=bar)]", "cfg(test)"},
		{"#[serde(rename = \"test_name\")]", "serde"},
		{"#[doc = \"test helpers\"]", "doc"},
		{"#![inner_attr]", "inner_attr"},
	}
	for _, tc := range cases {
		got := rustAttributeHeadName(tc.attr)
		if got != tc.want {
			t.Errorf("rustAttributeHeadName(%q) = %q, want %q", tc.attr, got, tc.want)
		}
	}
}

// TestIsRustTestRelatedAttr exercises the closed-set allowlist.
func TestIsRustTestRelatedAttr(t *testing.T) {
	allow := []string{
		"test", "tokio::test", "rstest", "test_case",
		"bench", "divan::bench", "criterion::benchmark",
		"cfg(test)", "cfg(fuzzing)",
	}
	for _, a := range allow {
		if !isRustTestRelatedAttr(a) {
			t.Errorf("isRustTestRelatedAttr(%q) = false; want true", a)
		}
	}
	deny := []string{
		"serde", "doc", "derive", "inline", "tokio", "test_helper",
		"cfg(target_arch)", "test_name", "", "cfg(unix)",
	}
	for _, a := range deny {
		if isRustTestRelatedAttr(a) {
			t.Errorf("isRustTestRelatedAttr(%q) = true; want false", a)
		}
	}
}

// TestClassifyTestKindRust exercises the predicate end-to-end via ChunkFile,
// hitting all allowlist variants + the negative regressions that motivated
// the head-name allowlist over substring matching.
func TestClassifyTestKindRust(t *testing.T) {
	cases := []struct {
		desc     string
		path     string
		src      string
		funcName string
		wantTest bool
		wantKind TestKind
	}{
		{
			desc: "#[test]",
			path: "src/lib.rs",
			src: `
#[test]
fn t1() {}
`,
			funcName: "t1",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "#[tokio::test]",
			path: "src/lib.rs",
			src: `
#[tokio::test]
async fn t2() {}
`,
			funcName: "t2",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "#[rstest]",
			path: "src/lib.rs",
			src: `
#[rstest]
fn t3() {}
`,
			funcName: "t3",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "#[test_case]",
			path: "src/lib.rs",
			src: `
#[test_case]
fn t4() {}
`,
			funcName: "t4",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "#[quickcheck]",
			path: "tests/qc.rs",
			src: `
#[quickcheck]
fn t5(xs: Vec<i32>) -> bool { true }
`,
			funcName: "t5",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "#[bench]",
			path: "src/lib.rs",
			src: `
#[bench]
fn b1(b: &mut Bencher) {}
`,
			funcName: "b1",
			wantTest: true, wantKind: TestKindBenchmark,
		},
		{
			desc: "#[divan::bench]",
			path: "src/lib.rs",
			src: `
#[divan::bench]
fn b2() {}
`,
			funcName: "b2",
			wantTest: true, wantKind: TestKindBenchmark,
		},
		{
			desc: "#[criterion::benchmark]",
			path: "src/lib.rs",
			src: `
#[criterion::benchmark]
fn b3(c: &mut Criterion) {}
`,
			funcName: "b3",
			wantTest: true, wantKind: TestKindBenchmark,
		},
		{
			desc: "#[cfg(fuzzing)] → Fuzz",
			path: "src/lib.rs",
			src: `
#[cfg(fuzzing)]
fn fz() {}
`,
			funcName: "fz",
			wantTest: true, wantKind: TestKindFuzz,
		},
		{
			desc: "stacked #[test] + #[ignore] → Test (priority)",
			path: "src/lib.rs",
			src: `
#[test]
#[ignore]
fn t_stack() {}
`,
			funcName: "t_stack",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "tests/ directory file with no attribute → helper",
			path: "tests/integration.rs",
			src: `
fn helper_fn() {}
`,
			funcName: "helper_fn",
			wantTest: true, wantKind: TestKindHelper,
		},
		{
			desc: "non-test file no attr → none",
			path: "src/lib.rs",
			src: `
fn plain_fn() {}
`,
			funcName: "plain_fn",
			wantTest: false, wantKind: TestKindNone,
		},
		// Negative regressions: false-positive substrings.
		{
			desc: "#[serde(rename = \"test_name\")] does NOT trigger",
			path: "src/lib.rs",
			src: `
#[serde(rename = "test_name")]
fn s_fn() {}
`,
			funcName: "s_fn",
			wantTest: false, wantKind: TestKindNone,
		},
		{
			desc: "#[doc = \"test helpers\"] does NOT trigger",
			path: "src/lib.rs",
			src: `
#[doc = "test helpers"]
fn d_fn() {}
`,
			funcName: "d_fn",
			wantTest: false, wantKind: TestKindNone,
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
				if res.Chunks[i].Name == tc.funcName {
					found = &res.Chunks[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("function %q not found", tc.funcName)
			}
			if found.IsTest != tc.wantTest {
				t.Errorf("IsTest = %v, want %v", found.IsTest, tc.wantTest)
			}
			if found.TestKind != tc.wantKind {
				t.Errorf("TestKind = %q, want %q", found.TestKind, tc.wantKind)
			}
		})
	}
}

// TestExtendFrameworksRust verifies FrameworkRustTest is appended for files
// with any test-related attribute, and NOT appended for files containing
// only false-positive substrings.
func TestExtendFrameworksRust(t *testing.T) {
	cases := []struct {
		desc    string
		path    string
		src     string
		wantHas bool
	}{
		{"plain #[bench] file", "src/lib.rs",
			"#[bench]\nfn b() {}\n", true},
		{"#[cfg(test)] mod tests", "src/lib.rs",
			"#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n", true},
		{"#[cfg(fuzzing)] only", "src/lib.rs",
			"#[cfg(fuzzing)]\nfn fz() {}\n", true},
		{"#[serde] only — NO FrameworkRustTest", "src/lib.rs",
			"#[serde(rename = \"test_name\")]\nfn s() {}\n", false},
		{"#[doc] only — NO FrameworkRustTest", "src/lib.rs",
			"#[doc = \"test helpers\"]\nfn d() {}\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()
			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			has := false
			for _, ch := range res.Chunks {
				for _, fw := range ch.Context.Frameworks {
					if fw == FrameworkRustTest {
						has = true
					}
				}
			}
			if has != tc.wantHas {
				t.Errorf("FrameworkRustTest present = %v, want %v", has, tc.wantHas)
			}
		})
	}
}
