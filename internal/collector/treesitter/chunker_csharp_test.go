// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"
)

// TestClassifyTestKindCSharp exercises NUnit / xUnit / MSTest /
// BenchmarkDotNet attribute dispatch end-to-end.
func TestClassifyTestKindCSharp(t *testing.T) {
	cases := []struct {
		desc     string
		path     string
		src      string
		method   string
		wantTest bool
		wantKind TestKind
	}{
		{
			desc: "NUnit [Test]",
			path: "Tests/FooTests.cs",
			src: `namespace Tests;
using NUnit.Framework;
public class FooTests {
    [Test]
    public void ShouldWork() {}
}
`,
			method:   "ShouldWork",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "xUnit [Fact]",
			path: "Tests/BarTests.cs",
			src: `namespace Tests;
using Xunit;
public class BarTests {
    [Fact]
    public void Works() {}
}
`,
			method:   "Works",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "xUnit [Theory]",
			path: "Tests/TheoryTests.cs",
			src: `namespace Tests;
public class TheoryTests {
    [Theory]
    public void Param() {}
}
`,
			method:   "Param",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "MSTest [TestMethod]",
			path: "Tests/MsTests.cs",
			src: `namespace Tests;
public class MsTests {
    [TestMethod]
    public void DoIt() {}
}
`,
			method:   "DoIt",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "BenchmarkDotNet [Benchmark]",
			path: "Tests/Bench.cs",
			src: `namespace Tests;
public class Bench {
    [Benchmark]
    public void Measure() {}
}
`,
			method:   "Measure",
			wantTest: true, wantKind: TestKindBenchmark,
		},
		{
			desc: "NUnit [SetUp]",
			path: "Tests/SetupTests.cs",
			src: `namespace Tests;
public class SetupTests {
    [SetUp]
    public void Init() {}
}
`,
			method:   "Init",
			wantTest: true, wantKind: TestKindSetup,
		},
		{
			desc: "MSTest [TestInitialize]",
			path: "Tests/MsInit.cs",
			src: `namespace Tests;
public class MsInit {
    [TestInitialize]
    public void Init() {}
}
`,
			method:   "Init",
			wantTest: true, wantKind: TestKindSetup,
		},
		{
			desc: "NUnit [TearDown]",
			path: "Tests/TearTests.cs",
			src: `namespace Tests;
public class TearTests {
    [TearDown]
    public void Clean() {}
}
`,
			method:   "Clean",
			wantTest: true, wantKind: TestKindTeardown,
		},
		{
			desc: "Dot-qualified short form",
			path: "Tests/QualifiedTests.cs",
			src: `namespace Tests;
public class QualifiedTests {
    [NUnit.Framework.Test]
    public void Qualified() {}
}
`,
			method:   "Qualified",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "Long form [TestAttribute] → helper (Q6: NOT matched)",
			path: "Tests/LongFormTests.cs",
			src: `namespace Tests;
public class LongFormTests {
    [TestAttribute]
    public void LongForm() {}
}
`,
			method:   "LongForm",
			wantTest: true, wantKind: TestKindHelper,
		},
		{
			desc: "no attribute → helper",
			path: "Tests/HelperTests.cs",
			src: `namespace Tests;
public class HelperTests {
    public void Plain() {}
}
`,
			method:   "Plain",
			wantTest: true, wantKind: TestKindHelper,
		},
		{
			desc: "non-test file → none",
			path: "src/Service.cs",
			src: `namespace Foo;
public class Service {
    [Test]
    public void Weird() {}
}
`,
			method:   "Weird",
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
				if res.Chunks[i].Name == tc.method {
					found = &res.Chunks[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("method %q not found", tc.method)
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
