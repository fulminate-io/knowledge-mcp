// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChunkFile_PopulatesFrameworks verifies the full pipeline:
// extractFileContext → DetectFrameworks → fileCtx propagation →
// emitDeclarationChunk Context assignment lands the framework set on
// per-chunk Context as expected.
func TestChunkFile_PopulatesFrameworks(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		src      string
		expect   Framework
	}{
		{
			name:     "java-junit5",
			filePath: "T.java",
			src: `package com.example;
import org.junit.jupiter.api.Test;
class T {
	@Test
	void t() {}
}
`,
			expect: FrameworkJavaJUnit5,
		},
		{
			name:     "js-jest",
			filePath: "spec.js",
			src: `import { describe, it } from 'jest';
function f() { return 1; }
`,
			expect: FrameworkJSJest,
		},
		{
			name:     "py-pytest",
			filePath: "test_x.py",
			src: `import pytest

def test_one():
    assert True
`,
			expect: FrameworkPyPyTest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()

			result, err := chunker.ChunkFile(context.Background(), tt.filePath, []byte(tt.src))
			require.NoError(t, err)
			require.NotEmpty(t, result.Chunks, "expected at least one chunk")

			found := false
			for _, ch := range result.Chunks {
				for _, fw := range ch.Context.Frameworks {
					if fw == tt.expect {
						found = true
					}
				}
			}
			assert.True(t, found, "expected at least one chunk's Context.Frameworks to include %q; got chunks=%d", tt.expect, len(result.Chunks))
		})
	}
}

// TestChunkFile_NoFrameworksWhenContextDisabled confirms the
// includeContext gate at chunker.go:317-319 still controls propagation —
// DetectFrameworks runs unconditionally, but the framework set must not
// leak onto chunk.Context when includeContext=false.
func TestChunkFile_NoFrameworksWhenContextDisabled(t *testing.T) {
	src := []byte(`import { describe, it } from 'jest';
function f() { return 1; }
`)
	chunker := NewChunker()
	chunker.config.includeContext = false
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "spec.js", src)
	require.NoError(t, err)
	require.NotEmpty(t, result.Chunks)

	for _, ch := range result.Chunks {
		assert.Nil(t, ch.Context.Frameworks, "Frameworks must be nil when includeContext is false")
		assert.Nil(t, ch.Context.Imports, "Imports must be nil when includeContext is false")
	}
}

// TestChunkFile_EmptyFrameworksForUntableLangs covers languages with no
// import-based detection (Ruby/Bash/HCL — empty Imports queries or no
// framework table). Framework set must be nil; Bucket B will populate
// these via AST signals — outside this ticket's scope.
func TestChunkFile_EmptyFrameworksForUntableLangs(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		src      string
	}{
		{
			name:     "ruby-no-imports-query",
			filePath: "spec.rb",
			src: `require 'rspec'
RSpec.describe "thing" do
  it "works" do
  end
end
`,
		},
		{
			name:     "bash-no-imports-query",
			filePath: "test.sh",
			src: `#!/usr/bin/env bash
hello() {
  echo "hi"
}
`,
		},
		{
			name:     "hcl-no-imports-query",
			filePath: "main.tf",
			src: `resource "aws_s3_bucket" "b" {
  bucket = "example"
}
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()

			result, err := chunker.ChunkFile(context.Background(), tt.filePath, []byte(tt.src))
			require.NoError(t, err)

			for _, ch := range result.Chunks {
				assert.Nil(t, ch.Context.Frameworks, "Frameworks must be nil for languages without a detection table")
			}
		})
	}
}
