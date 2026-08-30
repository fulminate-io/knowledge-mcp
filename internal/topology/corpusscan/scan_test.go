// SPDX-License-Identifier: Apache-2.0

package corpusscan

import (
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// wellFormedRequest is the KNOWN-POSITIVE CONTROL shared by every case below: a
// validator that refused everything would satisfy each negative assertion on its
// own, so each test also proves this request passes.
func wellFormedRequest() foundation.Request {
	return scanRequest(newFakeCaller(), "knowledge", "/tmp/repo")
}

func TestCorpusScan_RefusesNonCodeGraph(t *testing.T) {
	if _, err := validateScanRequest(wellFormedRequest()); err != nil {
		t.Fatalf("control: a well-formed request must validate, got %v", err)
	}
	req := wellFormedRequest()
	req.Graph = kgtypes.GraphKnowledge
	_, err := validateScanRequest(req)
	if err == nil {
		t.Fatal("a non-code graph must be refused, not silently skipped")
	}
	if !strings.Contains(err.Error(), string(kgtypes.GraphKnowledge)) {
		t.Errorf("the refusal must name the graph it received, got %q", err)
	}
}

func TestCorpusScan_RequiresLanguage(t *testing.T) {
	if _, err := validateScanRequest(wellFormedRequest()); err != nil {
		t.Fatalf("control: a well-formed request must validate, got %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*foundation.Request)
		want   string
	}{
		{"empty language", func(r *foundation.Request) { r.Language = "" }, "language is required"},
		{"empty repo", func(r *foundation.Request) { r.Name = "" }, "target repo is required"},
		{"empty repo root", func(r *foundation.Request) { r.RepoRoot = "" }, "working-directory root is required"},
		{"nil caller", func(r *foundation.Request) { r.Caller = nil }, "Caller must not be nil"},
	} {
		req := wellFormedRequest()
		tc.mutate(&req)
		_, err := validateScanRequest(req)
		if err == nil {
			t.Errorf("%s: must be refused", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refusal %q does not name the condition %q", tc.name, err, tc.want)
		}
	}
}

func TestCorpusScan_RejectsMalformedChecksSubset(t *testing.T) {
	// CONTROL: an absent key means "every check" and must NOT error, and a
	// well-formed list must parse to exactly its members. Without both, a parser
	// that rejected every subset would pass the negatives below.
	if got, err := parseChecksSubset(nil); err != nil || got != nil {
		t.Fatalf("control: an absent %s must mean every check, got %v / %v", ExtraKeyChecks, got, err)
	}
	got, err := parseChecksSubset(map[string]string{ExtraKeyChecks: "b, a"})
	if err != nil {
		t.Fatalf("control: a well-formed subset must parse, got %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("control: a well-formed subset must yield its trimmed sorted members, got %v", got)
	}

	for _, tc := range []struct{ name, raw string }{
		{"empty value", ""},
		{"whitespace only", "   "},
		{"trailing separator", "a,"},
		{"empty element", "a,,b"},
	} {
		if _, err := parseChecksSubset(map[string]string{ExtraKeyChecks: tc.raw}); err == nil {
			t.Errorf("%s: %q must be refused rather than silently widening the scan to the whole corpus", tc.name, tc.raw)
		}
	}
}
