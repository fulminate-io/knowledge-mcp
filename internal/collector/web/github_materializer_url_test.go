// SPDX-License-Identifier: Apache-2.0

package web

import "testing"

// TestParseGitHubURL_Shapes covers the three recognized URL shapes plus a
// representative set of negative cases.
func TestParseGitHubURL_Shapes(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantOK    bool
		wantOwner string
		wantRepo  string
		wantRef   string
		wantPath  string
		wantKind  githubURLKind
	}{
		{
			name:      "root_no_path",
			raw:       "https://github.com/kat-co/concurrency-in-go-src",
			wantOK:    true,
			wantOwner: "kat-co",
			wantRepo:  "concurrency-in-go-src",
			wantKind:  kindRoot,
		},
		{
			name:      "root_with_www_prefix",
			raw:       "https://www.github.com/kat-co/concurrency-in-go-src",
			wantOK:    true,
			wantOwner: "kat-co",
			wantRepo:  "concurrency-in-go-src",
			wantKind:  kindRoot,
		},
		{
			name:      "tree_with_ref_and_path",
			raw:       "https://github.com/kat-co/concurrency-in-go-src/tree/master/concurrency-patterns-in-go",
			wantOK:    true,
			wantOwner: "kat-co",
			wantRepo:  "concurrency-in-go-src",
			wantRef:   "master",
			wantPath:  "concurrency-patterns-in-go",
			wantKind:  kindTree,
		},
		{
			name:      "tree_no_path",
			raw:       "https://github.com/owner/repo/tree/main",
			wantOK:    true,
			wantOwner: "owner",
			wantRepo:  "repo",
			wantRef:   "main",
			wantKind:  kindTree,
		},
		{
			name:      "blob_with_file",
			raw:       "https://github.com/owner/repo/blob/main/pkg/foo.go",
			wantOK:    true,
			wantOwner: "owner",
			wantRepo:  "repo",
			wantRef:   "main",
			wantPath:  "pkg/foo.go",
			wantKind:  kindBlob,
		},
		{
			name:      "owner_repo_lowercased",
			raw:       "https://github.com/Org/Repo/tree/main",
			wantOK:    true,
			wantOwner: "org",
			wantRepo:  "repo",
			wantRef:   "main",
			wantKind:  kindTree,
		},
		{
			name:      "trailing_dot_git_stripped",
			raw:       "https://github.com/owner/repo.git",
			wantOK:    true,
			wantOwner: "owner",
			wantRepo:  "repo",
			wantKind:  kindRoot,
		},
		{name: "issues_rejected", raw: "https://github.com/owner/repo/issues/123"},
		{name: "pulls_rejected", raw: "https://github.com/owner/repo/pulls"},
		{name: "releases_rejected", raw: "https://github.com/owner/repo/releases/tag/v1.0.0"},
		{name: "actions_rejected", raw: "https://github.com/owner/repo/actions"},
		{name: "wiki_rejected", raw: "https://github.com/owner/repo/wiki"},
		{name: "blob_without_ref_rejected", raw: "https://github.com/owner/repo/blob"},
		{name: "blob_with_ref_no_path_rejected", raw: "https://github.com/owner/repo/blob/main"},
		{name: "tree_without_ref_rejected", raw: "https://github.com/owner/repo/tree"},
		{name: "scheme_http_rejected", raw: "http://github.com/owner/repo"},
		{name: "host_gitlab_rejected", raw: "https://gitlab.com/owner/repo"},
		{name: "host_raw_rejected", raw: "https://raw.githubusercontent.com/owner/repo/main/file.go"},
		{name: "host_codeload_rejected", raw: "https://codeload.github.com/owner/repo/tar.gz/main"},
		{name: "empty_owner_rejected", raw: "https://github.com//repo"},
		{name: "empty_repo_rejected", raw: "https://github.com/owner/"},
		{name: "garbage_rejected", raw: "not a url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := parseGitHubURL(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok mismatch: got=%v want=%v info=%+v", ok, tt.wantOK, info)
			}
			if !ok {
				return
			}
			if info.Owner != tt.wantOwner {
				t.Errorf("Owner=%q want=%q", info.Owner, tt.wantOwner)
			}
			if info.Repo != tt.wantRepo {
				t.Errorf("Repo=%q want=%q", info.Repo, tt.wantRepo)
			}
			if info.Ref != tt.wantRef {
				t.Errorf("Ref=%q want=%q", info.Ref, tt.wantRef)
			}
			if info.Path != tt.wantPath {
				t.Errorf("Path=%q want=%q", info.Path, tt.wantPath)
			}
			if info.Kind != tt.wantKind {
				t.Errorf("Kind=%v want=%v", info.Kind, tt.wantKind)
			}
		})
	}
}
