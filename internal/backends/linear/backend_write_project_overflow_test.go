// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// The rune-cap split between Linear's short `description` tagline (<=255
// RUNES) and its uncapped `content` markdown body, on both the create and the
// update path. Split out of backend_write_project_test.go to keep both files
// under the repository's 500-line ceiling; the fixtures they share
// (createProjectFixture, callInput) still live there.

// TestCreateProject_ASCIIOverflow_CapsDescriptionLosslessContent: a 300-rune
// ASCII summary must cap description at <=255 runes and preserve the FULL
// summary plus the body in content.
func TestCreateProject_ASCIIOverflow_CapsDescriptionLosslessContent(t *testing.T) {
	srv, calls := createProjectFixture(t)
	b := backendForServer(srv)
	summary := strings.Repeat("a", 300)
	body := "long markdown body"
	_, err := b.CreateProject(context.Background(), backends.ProjectCreateArgs{
		GroupKey: "ABC", Name: "P", Summary: summary, Description: body,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	in := callInput(t, calls, 1)
	desc, _ := in["description"].(string)
	if got := utf8.RuneCountInString(desc); got > 255 {
		t.Errorf("description = %d runes, want <=255", got)
	}
	content, _ := in["content"].(string)
	if !strings.Contains(content, summary) {
		t.Errorf("content does not contain the FULL summary (lossless overflow violated)")
	}
	if !strings.Contains(content, body) {
		t.Errorf("content does not contain the original Description body")
	}
}

// TestCreateProject_MultibyteOverflow_RuneCorrect: 300 em-dashes (U+2014, 3
// bytes / 1 rune each = 900 bytes). A byte cap would slice mid-rune and keep
// only ~85 em-dashes; the rune cap keeps exactly 255 runes of valid UTF-8 and
// preserves the full summary in content.
func TestCreateProject_MultibyteOverflow_RuneCorrect(t *testing.T) {
	srv, calls := createProjectFixture(t)
	b := backendForServer(srv)
	summary := strings.Repeat("—", 300) // 300 runes, 900 bytes
	_, err := b.CreateProject(context.Background(), backends.ProjectCreateArgs{
		GroupKey: "ABC", Name: "P", Summary: summary, Description: "body",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	in := callInput(t, calls, 1)
	desc, _ := in["description"].(string)
	if got := utf8.RuneCountInString(desc); got != 255 {
		t.Errorf("description = %d runes, want exactly 255", got)
	}
	if !utf8.ValidString(desc) {
		t.Errorf("description is not valid UTF-8 (byte cap sliced mid-rune)")
	}
	content, _ := in["content"].(string)
	if !strings.Contains(content, summary) {
		t.Errorf("content does not contain the FULL multibyte summary (lossless overflow violated)")
	}
}

// TestCreateProject_Overflow_EmptyBody_NoTrailingSeparator: when the summary
// overflows AND Description is empty, content must be exactly the full summary
// with NO trailing separator (reviewer T3).
func TestCreateProject_Overflow_EmptyBody_NoTrailingSeparator(t *testing.T) {
	srv, calls := createProjectFixture(t)
	b := backendForServer(srv)
	summary := strings.Repeat("—", 300)
	_, err := b.CreateProject(context.Background(), backends.ProjectCreateArgs{
		GroupKey: "ABC", Name: "P", Summary: summary, // Description empty.
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	in := callInput(t, calls, 1)
	content, _ := in["content"].(string)
	if content != summary {
		t.Errorf("content = %q, want exactly the summary with no trailing separator", content)
	}
}

// TestUpdateProject_MultibyteOverflow_RuneCorrect: a 300-em-dash diff.Summary
// must cap description at exactly 255 runes of valid UTF-8 and preserve the
// full summary in content.
func TestUpdateProject_MultibyteOverflow_RuneCorrect(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		`{"data":{"projectUpdate":{"project":{"id":"proj_uuid_1","state":""}}}}`,
	})
	b := backendForServer(srv)
	summary := strings.Repeat("—", 300)
	body := "markdown body"
	err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Summary: new(summary), Description: new(body)})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	in := callInput(t, calls, 0)
	desc, _ := in["description"].(string)
	if got := utf8.RuneCountInString(desc); got != 255 {
		t.Errorf("description = %d runes, want exactly 255", got)
	}
	if !utf8.ValidString(desc) {
		t.Errorf("description is not valid UTF-8 (byte cap sliced mid-rune)")
	}
	content, _ := in["content"].(string)
	if !strings.Contains(content, summary) {
		t.Errorf("content does not contain the FULL multibyte summary (lossless overflow violated)")
	}
	if !strings.Contains(content, body) {
		t.Errorf("content does not contain the diff body")
	}
}

// TestUpdateProject_Overflow_EmptyBody_NoTrailingSeparator: overflowing
// diff.Summary with nil diff.Description → content is exactly the full summary,
// no trailing separator (reviewer T3).
func TestUpdateProject_Overflow_EmptyBody_NoTrailingSeparator(t *testing.T) {
	srv, calls := scriptedServer(t, []string{
		`{"data":{"projectUpdate":{"project":{"id":"proj_uuid_1","state":""}}}}`,
	})
	b := backendForServer(srv)
	summary := strings.Repeat("—", 300)
	err := b.UpdateProject(context.Background(),
		backends.RemoteRef{ID: "proj_uuid_1"},
		backends.ProjectDiff{Summary: new(summary)}) // Description nil.
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	in := callInput(t, calls, 0)
	content, _ := in["content"].(string)
	if content != summary {
		t.Errorf("content = %q, want exactly the summary with no trailing separator", content)
	}
}
