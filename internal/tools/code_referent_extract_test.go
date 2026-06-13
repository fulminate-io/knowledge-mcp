// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestExtractCodeReferents_Positive asserts each recognized referent form is
// extracted verbatim as a node-ID string: a repo-relative source path, a
// path:Symbol form, and a path:Type.Method form.
func TestExtractCodeReferents_Positive(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		content string
		want    []string
	}{
		{
			name:    "repo-relative go path",
			content: "the fix lives in cmd/knowledge/internal/tools/wire.go today",
			want:    []string{"cmd/knowledge/internal/tools/wire.go"},
		},
		{
			name:    "repo-relative python path",
			content: "see src/app/handlers/auth.py for the guard",
			want:    []string{"src/app/handlers/auth.py"},
		},
		{
			name:    "repo-relative typescript path",
			content: "web/src/components/Button.ts renders it",
			want:    []string{"web/src/components/Button.ts"},
		},
		{
			name:    "path colon Symbol",
			content: "PersistBatch is at tools/wire_persist.go:PersistBatch on the client",
			want:    []string{"tools/wire_persist.go:PersistBatch"},
		},
		{
			name:    "path colon Type.Method",
			content: "the carrier kgwire/batchedge.go:BatchEdge.ToProto converts it",
			want:    []string{"kgwire/batchedge.go:BatchEdge.ToProto"},
		},
		{
			name:    "referent in summary",
			summary: "Edge drop bug in tools/wire_persist.go:persistBatchEdge projection",
			content: "prose with no referent",
			want:    []string{"tools/wire_persist.go:persistBatchEdge"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractCodeReferents(tc.summary, tc.content)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractCodeReferents() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestExtractCodeReferents_Negative asserts non-referent shapes yield nothing:
// prose, a bare URL, a markdown link (URL target), a content-leak fragment, and
// a file:line form (digit-only colon suffix).
func TestExtractCodeReferents_Negative(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		content string
	}{
		{
			name:    "prose only",
			content: "we should think about the edge metadata projection more carefully",
		},
		{
			name:    "bare URL",
			content: "see https://github.com/fulminate-io/knowledge-mcp/blob/main/cmd/tools/wire.go",
		},
		{
			name:    "markdown link to URL",
			content: "[the wire helper](https://example.com/pkg/wire.go) explains it",
		},
		{
			name:    "content-leak fragment",
			content: `{"type":"thought","summary":"a leaked json blob with no path"}`,
		},
		{
			name:    "file colon line number",
			content: "the bug is at wire.go:457 in the loop",
		},
		{
			name:    "bare filename no directory",
			content: "open wire.go and read it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractCodeReferents(tc.summary, tc.content)
			if len(got) != 0 {
				t.Fatalf("extractCodeReferents() = %#v, want empty", got)
			}
		})
	}
}

// TestExtractCodeReferents_DedupAndCap asserts a repeated referent yields one
// entry, and a body with more than codeReferentCap distinct referents yields
// exactly codeReferentCap entries in first-appearance order.
func TestExtractCodeReferents_DedupAndCap(t *testing.T) {
	t.Run("dedup repeated", func(t *testing.T) {
		body := "tools/wire.go is here, and again tools/wire.go, and tools/wire.go once more"
		got := extractCodeReferents("", body)
		want := []string{"tools/wire.go"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("dedup: got %#v, want %#v", got, want)
		}
	})

	t.Run("cap at codeReferentCap", func(t *testing.T) {
		// Build a body with codeReferentCap+5 distinct referents in a known order.
		const extra = 5
		var sb strings.Builder
		var wantAll []string
		for i := range codeReferentCap + extra {
			ref := fmt.Sprintf("pkg/dir%d/file.go", i)
			sb.WriteString(ref + " ")
			wantAll = append(wantAll, ref)
		}
		got := extractCodeReferents("", sb.String())
		if len(got) != codeReferentCap {
			t.Fatalf("cap: got %d referents, want %d", len(got), codeReferentCap)
		}
		want := wantAll[:codeReferentCap]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cap order: got %#v, want first %d in appearance order %#v",
				got, codeReferentCap, want)
		}
	})
}
