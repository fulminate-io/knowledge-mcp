// SPDX-License-Identifier: Apache-2.0

// Package calibration scores the corpus scan analyzer against the only real
// external ground truth available for this codebase: the CodeQL alerts raised
// on the public knowledge-mcp mirror, which covers exactly the client subtree.
//
// alerts.go is the ground-truth retrieval half. It declares a narrow interface
// over the code-scanning API, the AlertSite record one alert instance maps to,
// and the paging fetch that produces the corpus.
//
// COORDINATE SPACE: MIRROR. An alert's path and line are true of the MIRROR
// TREE AT THE ALERT'S OWN COMMIT and of nothing else. Measured against all five
// current alerts: every mirror line carries the flagged construct, and the same
// line number in this repo at HEAD carries a func declaration, a comment or a
// closing brace. Re-mapping alerts into this repo's line space would mis-locate
// five of five, so the scoring join stays in mirror coordinates and the
// mirror-to-internal path map (pathmap.go) exists for REPORTING only.
package calibration

import (
	"context"
	"fmt"
	"sort"

	gogithub "github.com/google/go-github/v68/github"
)

// codeScanningAPI abstracts the subset of github.CodeScanningService we use, so
// the hermetic tests can supply a fake without a network. *gogithub.CodeScanningService
// satisfies it as written.
type codeScanningAPI interface {
	ListAlertsForRepo(ctx context.Context, owner, repo string, opts *gogithub.AlertListOptions) ([]*gogithub.Alert, *gogithub.Response, error)
	ListAlertInstances(ctx context.Context, owner, repo string, id int64, opts *gogithub.AlertInstancesListOptions) ([]*gogithub.MostRecentInstance, *gogithub.Response, error)
}

// toolCodeQL is the only scanning tool whose alerts are this harness's ground
// truth. Other tools may be enabled on the mirror later and must not silently
// enter the corpus.
const toolCodeQL = "CodeQL"

// MirrorOwner and MirrorRepo name the public mirror this harness scores against.
const (
	MirrorOwner = "fulminate-io"
	MirrorRepo  = "knowledge-mcp"
)

// frozenCorpusPath is the committed ground truth, relative to this package
// directory (which is every test's working directory).
const frozenCorpusPath = "testdata/codeql_alerts.json"

// frozenAlertFloor is a KNOWN-POSITIVE CONTROL, not a target. Measured against
// the live mirror: alerts one through five all exist and all are state=fixed.
// GitHub does not delete fixed alerts, so this count can rise but cannot
// legitimately fall; a fetch returning fewer is a truncated fetch, not a
// smaller corpus. A floor rather than an equality, so a newly filed alert does
// not false-fail this gate against correct work.
const frozenAlertFloor = 5

// frozenRuleIDs are the two rule IDs measured on the live mirror. They are
// locked literals rather than a re-derived count for the same reason the floor
// is a floor: a fixed alert cannot be removed, so their presence can only
// become more true.
var frozenRuleIDs = []string{
	"go/allocation-size-overflow",
	"go/incorrect-integer-conversion",
}

// PathClass says which of the two trees a path exists in. It is declared here,
// beside AlertSite, because AlertSite carries a field of it; pathmap.go declares
// only the mapper functions and consumes this type.
type PathClass int

const (
	// PathMapped is a mirror path with a counterpart in this repo.
	PathMapped PathClass = iota
	// PathMirrorOnly exists only in the mirror (README, .github/, scripts/sync-assets.sh).
	PathMirrorOnly
	// PathInternalOnly exists only here (cmd/knowledge-server, deploy/, proto/).
	PathInternalOnly
)

// String renders a PathClass for reports and test failures.
func (c PathClass) String() string {
	switch c {
	case PathMapped:
		return "mapped"
	case PathMirrorOnly:
		return "mirror-only"
	case PathInternalOnly:
		return "internal-only"
	default:
		return fmt.Sprintf("PathClass(%d)", int(c))
	}
}

// AlertSite is one code-scanning alert INSTANCE — one (alert, commit, location)
// triple. An alert that recurs across commits yields one AlertSite per commit,
// because CommitSHA is what makes a site true of a particular tree.
type AlertSite struct {
	Number           int
	RuleID           string // e.g. "go/allocation-size-overflow"
	SecuritySeverity string // Alert.Rule.SecuritySeverityLevel
	State            string // "open" | "fixed" | "dismissed"
	Tool             string // Alert.Tool.Name; only "CodeQL" participates
	Category         string // instance Category, e.g. "/language:go"
	CommitSHA        string // instance CommitSHA — the tree this site is TRUE OF
	Ref              string
	MirrorPath       string // instance Location.Path, in MIRROR coordinates

	StartLine   int
	EndLine     int
	StartColumn int
	EndColumn   int

	// InternalPath and InternalClass are DERIVED, not fetched. They are
	// populated by the scoring pass via MapMirrorPath before a report is
	// rendered. FetchAlertSites and the frozen corpus leave them zero, so the
	// frozen artifact stays a faithful record of what the API returned rather
	// than a mix of fetched and derived values.
	InternalPath  string
	InternalClass PathClass
}

// FetchAlertSites pages every code-scanning alert for owner/repo and expands
// each CodeQL alert into one AlertSite per instance. The returned slice is
// sorted by (CommitSHA, MirrorPath, StartLine, Number) so a frozen artifact has
// a stable byte layout.
//
// Retrieval is serial: this is a single-repo read of a handful of records, so
// paging concurrently would add rate-limit risk for no wall-clock gain.
func FetchAlertSites(ctx context.Context, api codeScanningAPI, owner, repo string) ([]AlertSite, error) {
	// LEAVE State AT ITS ZERO VALUE. GitHub DOCUMENTS this endpoint's state
	// filter as defaulting to "open"; measured against the live mirror, that
	// documentation is wrong — omitting state returns all five alerts while
	// state="open" returns zero, because every alert on the mirror is currently
	// fixed. Passing State: "open" "to be explicit" yields an empty ground truth
	// and makes every downstream score pass vacuously.
	opts := &gogithub.AlertListOptions{
		ListOptions: gogithub.ListOptions{PerPage: 100},
	}

	var sites []AlertSite
	for {
		alerts, resp, err := api.ListAlertsForRepo(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("calibration: list code-scanning alerts for %s/%s: %w", owner, repo, err)
		}

		for _, alert := range alerts {
			if alert.GetTool().GetName() != toolCodeQL {
				continue
			}
			expanded, err := expandAlert(ctx, api, owner, repo, alert)
			if err != nil {
				return nil, err
			}
			sites = append(sites, expanded...)
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		// Page lives on both embedded option structs, so it is qualified.
		opts.ListOptions.Page = resp.NextPage
	}

	sort.Slice(sites, func(i, j int) bool {
		a, b := sites[i], sites[j]
		switch {
		case a.CommitSHA != b.CommitSHA:
			return a.CommitSHA < b.CommitSHA
		case a.MirrorPath != b.MirrorPath:
			return a.MirrorPath < b.MirrorPath
		case a.StartLine != b.StartLine:
			return a.StartLine < b.StartLine
		default:
			return a.Number < b.Number
		}
	})
	return sites, nil
}

// expandAlert pages one alert's instances and returns an AlertSite per instance.
func expandAlert(
	ctx context.Context,
	api codeScanningAPI,
	owner, repo string,
	alert *gogithub.Alert,
) ([]AlertSite, error) {
	number := alert.GetNumber()
	opts := &gogithub.AlertInstancesListOptions{
		ListOptions: gogithub.ListOptions{PerPage: 100},
	}

	var sites []AlertSite
	for {
		instances, resp, err := api.ListAlertInstances(ctx, owner, repo, int64(number), opts)
		if err != nil {
			return nil, fmt.Errorf("calibration: list instances of alert %d in %s/%s: %w", number, owner, repo, err)
		}

		// THE RULE ID COMES FROM Rule.ID, NOT FROM THE TOP-LEVEL RuleID FIELD.
		// Measured against the live mirror: the list endpoint omits rule_id
		// entirely and carries the identifier only under rule.id, so reading
		// Alert.RuleID yields the empty string for every alert and every
		// per-rule recall figure silently groups under "". Fail here rather
		// than freezing a corpus of blank rule ids.
		ruleID := alert.GetRule().GetID()
		if ruleID == "" {
			return nil, fmt.Errorf("calibration: alert %d in %s/%s carries no rule id", number, owner, repo)
		}

		for _, inst := range instances {
			loc := inst.GetLocation()
			if loc == nil {
				// A location-less instance is not a site with unknown
				// coordinates, it is a record this harness cannot score. Fail
				// naming the alert rather than emitting a zero-valued site.
				return nil, fmt.Errorf("calibration: alert %d in %s/%s has an instance with no location", number, owner, repo)
			}
			sites = append(sites, AlertSite{
				Number:           number,
				RuleID:           ruleID,
				SecuritySeverity: alert.GetRule().GetSecuritySeverityLevel(),
				State:            alert.GetState(),
				Tool:             alert.GetTool().GetName(),
				Category:         inst.GetCategory(),
				CommitSHA:        inst.GetCommitSHA(),
				Ref:              inst.GetRef(),
				MirrorPath:       loc.GetPath(),
				StartLine:        loc.GetStartLine(),
				EndLine:          loc.GetEndLine(),
				StartColumn:      loc.GetStartColumn(),
				EndColumn:        loc.GetEndColumn(),
			})
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return sites, nil
}
