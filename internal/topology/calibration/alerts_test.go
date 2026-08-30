// SPDX-License-Identifier: Apache-2.0

package calibration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	gogithub "github.com/google/go-github/v68/github"
)

// fakeCodeScanning is a hermetic stand-in for the code-scanning service. It
// serves canned alert pages, records every AlertListOptions it was handed, and
// returns one instance per alert unless instancesByNumber says otherwise.
type fakeCodeScanning struct {
	alertPages        [][]*gogithub.Alert
	instancesByNumber map[int][]*gogithub.MostRecentInstance

	seenAlertOpts []gogithub.AlertListOptions
}

func (f *fakeCodeScanning) ListAlertsForRepo(
	_ context.Context,
	_, _ string,
	opts *gogithub.AlertListOptions,
) ([]*gogithub.Alert, *gogithub.Response, error) {
	f.seenAlertOpts = append(f.seenAlertOpts, *opts)

	// opts.Page is zero on the first call and the previous response's NextPage
	// thereafter, so page 1 and page 0 are the same index.
	idx := opts.ListOptions.Page
	if idx > 0 {
		idx--
	}
	if idx >= len(f.alertPages) {
		return nil, &gogithub.Response{}, nil
	}
	resp := &gogithub.Response{}
	if idx+1 < len(f.alertPages) {
		resp.NextPage = idx + 2
	}
	return f.alertPages[idx], resp, nil
}

func (f *fakeCodeScanning) ListAlertInstances(
	_ context.Context,
	_, _ string,
	id int64,
	_ *gogithub.AlertInstancesListOptions,
) ([]*gogithub.MostRecentInstance, *gogithub.Response, error) {
	return f.instancesByNumber[int(id)], &gogithub.Response{}, nil
}

// fakeAlert builds a minimal CodeQL alert carrying the fields FetchAlertSites reads.
//
// THE TOP-LEVEL RuleID FIELD IS DELIBERATELY LEFT UNSET. Measured against the
// live mirror, the list endpoint omits rule_id entirely and carries the
// identifier only under rule.id; a fake that populated both would let a read of
// the wrong field pass here and produce a corpus of blank rule ids in the field.
func fakeAlert(number int, ruleID string) *gogithub.Alert {
	return &gogithub.Alert{
		Number: new(number),
		State:  new("fixed"),
		Rule: &gogithub.Rule{
			ID:                    new(ruleID),
			SecuritySeverityLevel: new("high"),
		},
		Tool: &gogithub.Tool{Name: new(toolCodeQL)},
	}
}

// fakeInstance builds one instance at a path and line in mirror coordinates.
func fakeInstance(sha, path string, line int) *gogithub.MostRecentInstance {
	return &gogithub.MostRecentInstance{
		Ref:       new("refs/heads/main"),
		Category:  new("/language:go"),
		CommitSHA: new(sha),
		Location: &gogithub.Location{
			Path:        new(path),
			StartLine:   new(line),
			EndLine:     new(line),
			StartColumn: new(2),
			EndColumn:   new(40),
		},
	}
}

// TestFetchAlertSites_Paginates catches a single-page fetch, which would
// silently under-report the ground truth and flatter every recall figure.
func TestFetchAlertSites_Paginates(t *testing.T) {
	fake := &fakeCodeScanning{
		alertPages: [][]*gogithub.Alert{
			{fakeAlert(1, "go/allocation-size-overflow")},
			{fakeAlert(2, "go/incorrect-integer-conversion")},
		},
		instancesByNumber: map[int][]*gogithub.MostRecentInstance{
			1: {fakeInstance("aaaa1111", "internal/tools/tools_logs_search.go", 276)},
			2: {fakeInstance("bbbb2222", "internal/collector/pdf/font/glyphlist.go", 103)},
		},
	}

	sites, err := FetchAlertSites(context.Background(), fake, MirrorOwner, MirrorRepo)
	if err != nil {
		t.Fatalf("FetchAlertSites: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("expected 2 sites across both pages, got %d: %+v", len(sites), sites)
	}
	if len(fake.seenAlertOpts) != 2 {
		t.Fatalf("expected 2 alert-list calls (one per page), got %d", len(fake.seenAlertOpts))
	}
	if got := fake.seenAlertOpts[1].ListOptions.Page; got != 2 {
		t.Fatalf("second call should follow NextPage=2, got Page=%d", got)
	}

	seen := map[int]bool{}
	for _, s := range sites {
		seen[s.Number] = true
	}
	for _, want := range []int{1, 2} {
		if !seen[want] {
			t.Fatalf("alert %d from its page is missing from the result: %+v", want, sites)
		}
	}
}

// TestFetchAlertSites_SendsEmptyStateFilter is the catcher for a measured
// documentation error: GitHub documents the state filter as defaulting to
// "open", but every alert on the mirror is fixed, so an explicit State: "open"
// returns an empty ground truth and makes every downstream score vacuous.
func TestFetchAlertSites_SendsEmptyStateFilter(t *testing.T) {
	fake := &fakeCodeScanning{
		alertPages: [][]*gogithub.Alert{
			{fakeAlert(1, "go/allocation-size-overflow")},
		},
		instancesByNumber: map[int][]*gogithub.MostRecentInstance{
			1: {fakeInstance("aaaa1111", "internal/tools/tools_logs_search.go", 276)},
		},
	}

	if _, err := FetchAlertSites(context.Background(), fake, MirrorOwner, MirrorRepo); err != nil {
		t.Fatalf("FetchAlertSites: %v", err)
	}
	if len(fake.seenAlertOpts) == 0 {
		t.Fatal("the fake recorded no alert-list call, so this test measured nothing")
	}
	for i, opts := range fake.seenAlertOpts {
		if opts.State != "" {
			t.Fatalf("call %d sent state=%q; it must be empty or the corpus is silently zero", i, opts.State)
		}
	}
}

// TestFetchAlertSites_ReadsRuleIDFromRule pins the field the identifier is
// actually read from. The first frozen corpus generated against the live mirror
// held five sites with an EMPTY RuleID because the code read Alert.RuleID, which
// the list endpoint never populates. Both halves are asserted: an alert carrying
// rule.id yields that id, and an alert carrying neither is a hard error rather
// than a site with a blank rule that every per-rule figure would group under "".
func TestFetchAlertSites_ReadsRuleIDFromRule(t *testing.T) {
	fake := &fakeCodeScanning{
		alertPages: [][]*gogithub.Alert{
			{fakeAlert(1, "go/allocation-size-overflow")},
		},
		instancesByNumber: map[int][]*gogithub.MostRecentInstance{
			1: {fakeInstance("aaaa1111", "internal/tools/tools_logs_search.go", 276)},
		},
	}
	sites, err := FetchAlertSites(context.Background(), fake, MirrorOwner, MirrorRepo)
	if err != nil {
		t.Fatalf("FetchAlertSites: %v", err)
	}
	if len(sites) != 1 {
		t.Fatalf("expected 1 site, got %d", len(sites))
	}
	if sites[0].RuleID != "go/allocation-size-overflow" {
		t.Fatalf("RuleID must come from rule.id; got %q", sites[0].RuleID)
	}

	ruleless := fakeAlert(2, "")
	ruleless.Rule = nil
	blind := &fakeCodeScanning{
		alertPages: [][]*gogithub.Alert{{ruleless}},
		instancesByNumber: map[int][]*gogithub.MostRecentInstance{
			2: {fakeInstance("aaaa1111", "internal/tools/tools_logs_search.go", 276)},
		},
	}
	if _, err := FetchAlertSites(context.Background(), blind, MirrorOwner, MirrorRepo); err == nil {
		t.Fatal("expected an error for an alert with no rule id")
	}
}

// TestFetchAlertSites_SkipsNonCodeQLTools proves the tool filter discriminates
// rather than admitting everything: a non-CodeQL alert beside a CodeQL one must
// drop out while the CodeQL one survives.
func TestFetchAlertSites_SkipsNonCodeQLTools(t *testing.T) {
	other := fakeAlert(9, "other/rule")
	other.Tool = &gogithub.Tool{Name: new("SomeOtherScanner")}

	fake := &fakeCodeScanning{
		alertPages: [][]*gogithub.Alert{
			{fakeAlert(1, "go/allocation-size-overflow"), other},
		},
		instancesByNumber: map[int][]*gogithub.MostRecentInstance{
			1: {fakeInstance("aaaa1111", "internal/tools/tools_logs_search.go", 276)},
			9: {fakeInstance("aaaa1111", "internal/tools/tools_logs_search.go", 9)},
		},
	}

	sites, err := FetchAlertSites(context.Background(), fake, MirrorOwner, MirrorRepo)
	if err != nil {
		t.Fatalf("FetchAlertSites: %v", err)
	}
	if len(sites) != 1 || sites[0].Number != 1 {
		t.Fatalf("expected only the CodeQL alert to survive, got %+v", sites)
	}
}

// TestFetchAlertSites_RejectsLocationlessInstance proves a location-less
// instance is a hard error naming the alert, not a zero-valued site that would
// join against line 0 of some file.
func TestFetchAlertSites_RejectsLocationlessInstance(t *testing.T) {
	fake := &fakeCodeScanning{
		alertPages: [][]*gogithub.Alert{
			{fakeAlert(7, "go/allocation-size-overflow")},
		},
		instancesByNumber: map[int][]*gogithub.MostRecentInstance{
			7: {{CommitSHA: new("aaaa1111")}},
		},
	}

	_, err := FetchAlertSites(context.Background(), fake, MirrorOwner, MirrorRepo)
	if err == nil {
		t.Fatal("expected an error for an instance with no location")
	}
	if !strings.Contains(err.Error(), "alert 7") {
		t.Fatalf("error must name the alert number, got: %v", err)
	}
}

// TestFrozenAlertCorpusMatchesConstant guards the committed ground truth. It is
// ALWAYS ON and reads only the committed artifact — no network, no daemon, no
// env — so it behaves identically in this repo, in the public mirror's CI, and
// on a developer machine that is offline.
//
// What it deliberately does NOT assert: whether the frozen artifact still
// matches the live mirror. A live-drift assertion would make CI depend on an
// external service. The live fetch reports drift; only an operator acts on it.
func TestFrozenAlertCorpusMatchesConstant(t *testing.T) {
	raw, err := os.ReadFile(frozenCorpusPath)
	if err != nil {
		t.Fatalf("read frozen corpus: %v (generate it with TestFreezeAlertCorpusLive, then commit it)", err)
	}
	var sites []AlertSite
	if err := json.Unmarshal(raw, &sites); err != nil {
		t.Fatalf("unmarshal frozen corpus: %v", err)
	}

	if len(sites) < frozenAlertFloor {
		t.Fatalf("frozen corpus holds %d sites, below the measured floor of %d — a fixed alert cannot be removed, so this is a truncated fetch rather than a smaller corpus", len(sites), frozenAlertFloor)
	}

	// A corpus of well-formed-but-empty records satisfies a bare length check,
	// so every site is checked for the fields a join actually consumes.
	for i, s := range sites {
		if s.RuleID == "" || s.MirrorPath == "" || s.CommitSHA == "" {
			t.Fatalf("site %d is missing a required field: %+v", i, s)
		}
		if s.Tool != toolCodeQL {
			t.Fatalf("site %d carries tool %q; only %s is ground truth", i, s.Tool, toolCodeQL)
		}
	}

	for _, want := range frozenRuleIDs {
		found := false
		for _, s := range sites {
			if s.RuleID == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("frozen corpus is missing measured rule %q", want)
		}
	}
}
