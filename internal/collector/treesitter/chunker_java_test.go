// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"strings"
	"testing"
)

// TestIsJavaTestFile covers the Maven/Gradle layout + naming conventions.
func TestIsJavaTestFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"src/test/java/com/example/FooTest.java", true},
		{"src/test/java/com/example/FooTests.java", true},
		{"src/main/java/com/example/FooIT.java", true}, // basename match
		{"src/test/java/com/example/Foo.java", true},   // src/test/java match
		{"src/main/java/com/example/Foo.java", false},
		{"app/src/test/java/com/example/Helper.java", true},
	}
	for _, tc := range cases {
		got := isJavaTestFile(tc.path)
		if got != tc.want {
			t.Errorf("isJavaTestFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestClassifyTestKindJava drives the predicate end-to-end via ChunkFile so
// we exercise the actual tree-sitter Java grammar's `modifiers` node shape
// and confirm extractJVMAnnotations sees the @Test annotations.
func TestClassifyTestKindJava(t *testing.T) {
	cases := []struct {
		desc     string
		path     string
		src      string
		method   string
		wantTest bool
		wantKind TestKind
	}{
		{
			desc: "JUnit 5 @Test",
			path: "src/test/java/com/example/FooTest.java",
			src: `package com.example;
import org.junit.jupiter.api.Test;
class FooTest {
	@Test
	void shouldWork() {}
}
`,
			method:   "shouldWork",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "JUnit 5 @ParameterizedTest",
			path: "src/test/java/com/example/ParamTest.java",
			src: `package com.example;
import org.junit.jupiter.params.ParameterizedTest;
class ParamTest {
	@ParameterizedTest
	void paramCase() {}
}
`,
			method:   "paramCase",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "JUnit 4 @Before",
			path: "src/test/java/com/example/SetupTest.java",
			src: `package com.example;
import org.junit.Before;
class SetupTest {
	@Before
	void setUp() {}
}
`,
			method:   "setUp",
			wantTest: true, wantKind: TestKindSetup,
		},
		{
			desc: "JUnit 5 @BeforeAll",
			path: "src/test/java/com/example/SetupTest.java",
			src: `package com.example;
import org.junit.jupiter.api.BeforeAll;
class SetupTest {
	@BeforeAll
	void setUpAll() {}
}
`,
			method:   "setUpAll",
			wantTest: true, wantKind: TestKindSetup,
		},
		{
			desc: "JUnit 5 @AfterEach",
			path: "src/test/java/com/example/TeardownTest.java",
			src: `package com.example;
import org.junit.jupiter.api.AfterEach;
class TeardownTest {
	@AfterEach
	void cleanUp() {}
}
`,
			method:   "cleanUp",
			wantTest: true, wantKind: TestKindTeardown,
		},
		{
			desc: "TestNG @DataProvider → fixture",
			path: "src/test/java/com/example/DataTest.java",
			src: `package com.example;
import org.testng.annotations.DataProvider;
class DataTest {
	@DataProvider
	Object[][] provideData() { return null; }
}
`,
			method:   "provideData",
			wantTest: true, wantKind: TestKindFixture,
		},
		{
			desc: "JMH @Benchmark",
			path: "src/test/java/com/example/Bench.java",
			src: `package com.example;
import org.openjdk.jmh.annotations.Benchmark;
class Bench {
	@Benchmark
	void measure() {}
}
`,
			method:   "measure",
			wantTest: true, wantKind: TestKindBenchmark,
		},
		{
			desc: "FQN @org.junit.jupiter.api.Test",
			path: "src/test/java/com/example/FqnTest.java",
			src: `package com.example;
class FqnTest {
	@org.junit.jupiter.api.Test
	void fqn() {}
}
`,
			method:   "fqn",
			wantTest: true, wantKind: TestKindTest,
		},
		{
			desc: "no annotation → helper in test file",
			path: "src/test/java/com/example/HelperTest.java",
			src: `package com.example;
class HelperTest {
	void plainMethod() {}
}
`,
			method:   "plainMethod",
			wantTest: true, wantKind: TestKindHelper,
		},
		{
			desc: "@Test in non-test file → none",
			path: "src/main/java/com/example/Service.java",
			src: `package com.example;
import org.junit.jupiter.api.Test;
class Service {
	@Test
	void weirdAnnotated() {}
}
`,
			method:   "weirdAnnotated",
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
				// Print method names that DID surface for diagnostics.
				var names []string
				for _, ch := range res.Chunks {
					names = append(names, ch.ChunkType+":"+ch.Name)
				}
				t.Fatalf("method %q not found in chunks: %s", tc.method, strings.Join(names, ", "))
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

// TestClassifyTestKindJava_MockFile verifies *Mock.java filename → TestKindMock.
func TestClassifyTestKindJava_MockFile(t *testing.T) {
	src := `package com.example;
class FooMock {
	void doNothing() {}
}
`
	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), "src/main/java/com/example/FooMock.java", []byte(src))
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	for _, ch := range res.Chunks {
		if !ch.IsTest {
			t.Errorf("chunk %q (%s): IsTest=false; want true (mock file)", ch.Name, ch.ChunkType)
		}
		if ch.TestKind != TestKindMock {
			t.Errorf("chunk %q (%s): TestKind=%q; want %q", ch.Name, ch.ChunkType, ch.TestKind, TestKindMock)
		}
	}
}
