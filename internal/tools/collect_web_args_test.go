// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"
)

// TestCollectArgs_WebPathSegmentAndPerHostUnmarshal pins the schema-key →
// struct-field contract for the two crawl-scoping knobs: a collect-tool JSON
// payload carrying max_path_segments / max_pages_per_host must populate the
// matching collectArgs fields so they can flow into web.CrawlOptions.
// collectArgs is unexported, so this test must live in package tools.
func TestCollectArgs_WebPathSegmentAndPerHostUnmarshal(t *testing.T) {
	payload := `{
		"type": "web",
		"id": "web/example",
		"seed_urls": ["https://example.com/"],
		"max_depth": 3,
		"max_path_segments": 5,
		"max_pages_per_host": 12
	}`

	var a collectArgs
	if err := json.Unmarshal([]byte(payload), &a); err != nil {
		t.Fatalf("unmarshal collect web payload: %v", err)
	}

	if a.MaxPathSegments != 5 {
		t.Errorf("MaxPathSegments: got %d, want 5", a.MaxPathSegments)
	}
	if a.MaxPagesPerHost != 12 {
		t.Errorf("MaxPagesPerHost: got %d, want 12", a.MaxPagesPerHost)
	}
}
