// SPDX-License-Identifier: Apache-2.0

// score.go joins the corpus scanner's findings to the CodeQL ground truth and
// produces the measurement. render.go owns how that measurement is DISPLAYED;
// the split is deliberate, because a rule about presentation needs a symbol that
// owns the presentation.
//
// THE JOIN HAPPENS IN MIRROR COORDINATES AT A NAMED COMMIT, and Score refuses
// input expressed in this repo's coordinates rather than silently remapping it.
// The reason is measured: read at each alert's own commit, all five ground-truth
// lines carry the flagged construct, while the same line numbers in this repo at
// HEAD carry a func declaration, a comment or a closing brace. A silent remap
// would manufacture agreement rather than translate a coordinate.
//
// TWO GRANULARITIES ARE NEVER BLENDED. A site claim carries a file AND a line
// and is scored at line granularity; a file claim carries a file with the line
// key deliberately ABSENT and is scored at file granularity. A file-granular hit
// is the weaker claim, and averaging the two yields a figure that means neither
// thing.
//
// UNDEFINED IS A RESULT, NOT A NUMBER. A precision with no site claims and a
// recall with no ground-truth positives are both undefined, carried as flags and
// rendered as phrases. At the mirror's current HEAD the ground truth is empty
// for every rule, so a harness that rendered 0/0 as a percentage would publish a
// confidently false measurement on the most likely input it will ever receive.
package calibration

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// The scan-analyzer finding-contract keys. These are DECLARED HERE rather than
// imported from the producer, deliberately: the second declaration plus an
// equality assertion in the contract test is what catches a respelling on either
// side, where a direct import would only catch absence. The failure this guards
// is not "the constant is missing" — it is "both sides are internally consistent
// and join zero rows".
const (
	// MetaKeyFile is the repo-relative path of the flagged site.
	MetaKeyFile = "file"
	// MetaKeyLine is the 1-based line of the flagged site, as a decimal string.
	MetaKeyLine = "line"
	// MetaKeyCheckID is the corpus check's identity.
	MetaKeyCheckID = "check_id"
)

// ClaimKind is what a finding claims about code, decided by which metadata keys
// are PRESENT rather than by what they contain.
type ClaimKind int

const (
	// ClaimSite carries a file AND a line: a line-granular claim.
	ClaimSite ClaimKind = iota
	// ClaimFile carries a file with the line key ABSENT: a file-granular claim.
	// An absent line means "we cannot say which line", never line zero.
	ClaimFile
	// ClaimNonSite carries no file: a refusal, or a per-check or run-level
	// truncation notice. Not a claim about code.
	ClaimNonSite
)

// internalOnlyPrefixes are the top-level prefixes under which the MIRROR HAS NO
// GO FILE. The mirror's entire Go surface is internal/, gen/ and the root
// main.go, so a finding — always about a Go file — carrying one of these is
// proof the caller handed over internal-space input.
//
// IDENTITY-MAPPED PREFIXES ARE DELIBERATELY ABSENT from this list: gen/,
// docs/guides/ and .claude/ map to themselves, so a finding under one of them is
// equally valid in both spaces and is evidence of nothing.
//
// The claim is about the GO surface specifically, not the mirror as a whole —
// the mirror does carry non-Go files under scripts/, such as its own
// sync-assets script.
var internalOnlyPrefixes = []string{"cmd/", "deploy/", "proto/", "scripts/"}

// CheckScore is one check's precision, at line granularity only.
type CheckScore struct {
	CheckID          string
	SiteClaims       int     // findings from this check carrying file AND line
	FileClaims       int     // findings carrying file but no line
	SiteMatched      int     // site claims landing inside an alert's line range
	FileMatched      int     // file claims landing in a file that carries an alert
	Precision        float64 // SiteMatched / SiteClaims — LINE granularity ONLY
	PrecisionDefined bool    // false when SiteClaims == 0
}

// RuleScore is one CodeQL rule's recall, reported at both granularities.
type RuleScore struct {
	RuleID            string
	GroundTruth       int
	LineHit           int
	FileHit           int
	Recall            float64 // LineHit / GroundTruth
	RecallDefined     bool
	FileRecall        float64 // FileHit / GroundTruth
	FileRecallDefined bool
}

// ScoreReport is one commit's measurement, self-contained: every value
// RenderReport displays is carried here, including the internal counterparts,
// so the renderer needs no access to the path mapper.
type ScoreReport struct {
	CommitSHA string
	// CheckKinds are the kinds the CALLER declares were executed. EMPTY means
	// UNDECLARED — never "all". Nothing in the three-key finding contract
	// carries a check kind, so this cannot be derived from the findings.
	CheckKinds []string
	Checks     []CheckScore
	Rules      []RuleScore
	Unmatched  []AlertSite          // CodeQL flagged it, we did not
	Extra      []foundation.Finding // unmatched SITE claims — we flagged a line, CodeQL did not
	ExtraFile  []foundation.Finding // unmatched FILE claims, kept separate from Extra
	NonSite    []foundation.Finding // refusals and truncation notices
	JoinedZero bool
}

// claim is one classified finding, reduced to what the join consumes.
type claim struct {
	kind    ClaimKind
	file    string
	line    int
	checkID string
	finding foundation.Finding
}

// classifyClaim decides a finding's kind from key PRESENCE, and errors on the
// two malformed shapes rather than guessing at them.
//
// A check id is NOT required to reach ClaimNonSite: the run-level truncation
// notice carries no Metadata at all, and requiring one there would error on a
// correct producer.
func classifyClaim(f foundation.Finding) (claim, error) {
	md := f.Metadata
	checkID := md[MetaKeyCheckID]
	file, hasFile := md[MetaKeyFile]
	rawLine, hasLine := md[MetaKeyLine]

	if !hasFile {
		if hasLine {
			return claim{}, fmt.Errorf("calibration: finding for check %q carries %s=%q with no %s — a line without a file is malformed", checkID, MetaKeyLine, rawLine, MetaKeyFile)
		}
		return claim{kind: ClaimNonSite, checkID: checkID, finding: f}, nil
	}
	if !hasLine {
		return claim{kind: ClaimFile, file: file, checkID: checkID, finding: f}, nil
	}
	// An EMPTY-STRING line is malformed input, not a missing key. The producer
	// OMITS the key to mean file-granular, so a present-but-empty value is a
	// different statement and is refused rather than read as absence.
	if strings.TrimSpace(rawLine) == "" {
		return claim{}, fmt.Errorf("calibration: finding for check %q carries an empty %s — the key is OMITTED to mean file-granular, never blanked", checkID, MetaKeyLine)
	}
	line, err := strconv.Atoi(rawLine)
	if err != nil {
		return claim{}, fmt.Errorf("calibration: finding for check %q carries %s=%q, which is not a decimal integer: %w", checkID, MetaKeyLine, rawLine, err)
	}
	return claim{kind: ClaimSite, file: file, line: line, checkID: checkID, finding: f}, nil
}

// guardCoordinateSpace refuses a claim expressed in this repo's coordinates.
func guardCoordinateSpace(c claim) error {
	for _, p := range internalOnlyPrefixes {
		if strings.HasPrefix(c.file, p) {
			return fmt.Errorf("calibration: finding for check %q carries %s=%q, which is in THIS repo's coordinates — the mirror has no Go file under %q, and scoring joins in mirror coordinates only", c.checkID, MetaKeyFile, c.file, p)
		}
	}
	return nil
}

// Score joins findings to the ground truth at commitSHA and returns the
// measurement. checkKinds is what the caller declares it executed; Score copies,
// deduplicates and sorts it, and NEVER invents one.
func Score(alerts []AlertSite, findings []foundation.Finding, commitSHA string, checkKinds []string) (ScoreReport, error) {
	report := ScoreReport{CommitSHA: commitSHA, CheckKinds: normalizeCheckKinds(checkKinds)}

	// Ground truth is COMMIT-SCOPED: an alert at another commit is not a miss,
	// it is not part of this run's truth at all.
	truth := make([]AlertSite, 0, len(alerts))
	byPath := map[string][]int{}
	for _, a := range alerts {
		if a.CommitSHA != commitSHA {
			continue
		}
		a.InternalPath, a.InternalClass, _ = MapMirrorPath(a.MirrorPath)
		byPath[a.MirrorPath] = append(byPath[a.MirrorPath], len(truth))
		truth = append(truth, a)
	}

	claims, err := classifyAll(findings, &report)
	if err != nil {
		return ScoreReport{}, err
	}

	j := newJoiner(truth, byPath)
	for _, c := range claims {
		j.add(c, &report)
	}

	lineHit, fileHit := j.lineHit, j.fileHit
	report.Checks = finalizeChecks(j.checks)
	report.Rules = finalizeRules(truth, lineHit, fileHit)
	for i, a := range truth {
		if !lineHit[i] && !fileHit[i] {
			report.Unmatched = append(report.Unmatched, a)
		}
	}
	sort.Slice(report.Unmatched, func(i, j int) bool {
		if report.Unmatched[i].MirrorPath != report.Unmatched[j].MirrorPath {
			return report.Unmatched[i].MirrorPath < report.Unmatched[j].MirrorPath
		}
		return report.Unmatched[i].StartLine < report.Unmatched[j].StartLine
	})

	// A zero join with claims on one side and truth on the other is far more
	// likely to mean the two are not in the same coordinate space than that the
	// scanner genuinely agrees with nothing. Non-site findings do not arm it: a
	// run of pure refusals is a refused run, which the ledger already says.
	report.JoinedZero = j.anyFileClaim && len(truth) > 0 && !j.anyJoin
	return report, nil
}

// joiner accumulates the two-granularity join. It is a type rather than an
// inline loop so each granularity's bookkeeping stays legible: blending them is
// the one mistake this scorer must not make.
type joiner struct {
	truth   []AlertSite
	byPath  map[string][]int
	lineHit []bool
	fileHit []bool
	checks  map[string]*CheckScore

	anyFileClaim bool
	anyJoin      bool
}

func newJoiner(truth []AlertSite, byPath map[string][]int) *joiner {
	return &joiner{
		truth:   truth,
		byPath:  byPath,
		lineHit: make([]bool, len(truth)),
		fileHit: make([]bool, len(truth)),
		checks:  map[string]*CheckScore{},
	}
}

// add scores one claim into its check's tally and into the hit vectors.
func (j *joiner) add(c claim, report *ScoreReport) {
	j.anyFileClaim = true
	cs := j.checks[c.checkID]
	if cs == nil {
		cs = &CheckScore{CheckID: c.checkID}
		j.checks[c.checkID] = cs
	}
	matched := j.mark(c)
	if matched {
		j.anyJoin = true
	}
	if c.kind == ClaimSite {
		cs.SiteClaims++
		if matched {
			cs.SiteMatched++
			return
		}
		report.Extra = append(report.Extra, c.finding)
		return
	}
	cs.FileClaims++
	if matched {
		cs.FileMatched++
		return
	}
	report.ExtraFile = append(report.ExtraFile, c.finding)
}

// mark records the hit against every ground-truth site on the claim's path, at
// the claim's own granularity, and reports whether anything matched.
func (j *joiner) mark(c claim) bool {
	matched := false
	for _, idx := range j.byPath[c.file] {
		if c.kind == ClaimSite {
			site := j.truth[idx]
			if c.line >= site.StartLine && c.line <= site.EndLine {
				j.lineHit[idx], matched = true, true
			}
			continue
		}
		j.fileHit[idx], matched = true, true
	}
	return matched
}

// classifyAll classifies every finding, routing non-site findings to the ledger
// and coordinate-guarding the rest.
func classifyAll(findings []foundation.Finding, report *ScoreReport) ([]claim, error) {
	claims := make([]claim, 0, len(findings))
	for _, f := range findings {
		c, err := classifyClaim(f)
		if err != nil {
			return nil, err
		}
		if c.kind == ClaimNonSite {
			// Never a denominator, never silently dropped: counting a refusal
			// as imprecision would defame a scan that correctly refused an
			// unvalidated check.
			report.NonSite = append(report.NonSite, f)
			continue
		}
		if err := guardCoordinateSpace(c); err != nil {
			return nil, err
		}
		claims = append(claims, c)
	}
	return claims, nil
}

// finalizeChecks computes each check's precision and sorts by check id.
func finalizeChecks(checks map[string]*CheckScore) []CheckScore {
	out := make([]CheckScore, 0, len(checks))
	for _, cs := range checks {
		if cs.SiteClaims > 0 {
			cs.Precision = float64(cs.SiteMatched) / float64(cs.SiteClaims)
			cs.PrecisionDefined = true
		}
		out = append(out, *cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CheckID < out[j].CheckID })
	return out
}

// finalizeRules computes per-rule recall at both granularities and sorts by rule id.
func finalizeRules(truth []AlertSite, lineHit, fileHit []bool) []RuleScore {
	rules := map[string]*RuleScore{}
	for i, a := range truth {
		rs := rules[a.RuleID]
		if rs == nil {
			rs = &RuleScore{RuleID: a.RuleID}
			rules[a.RuleID] = rs
		}
		rs.GroundTruth++
		if lineHit[i] {
			rs.LineHit++
		}
		if fileHit[i] {
			rs.FileHit++
		}
	}
	out := make([]RuleScore, 0, len(rules))
	for _, rs := range rules {
		if rs.GroundTruth > 0 {
			rs.Recall = float64(rs.LineHit) / float64(rs.GroundTruth)
			rs.RecallDefined = true
			rs.FileRecall = float64(rs.FileHit) / float64(rs.GroundTruth)
			rs.FileRecallDefined = true
		}
		out = append(out, *rs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out
}

// normalizeCheckKinds deduplicates and sorts the caller's declaration. A nil or
// empty input yields an empty result: NO DEFAULT IS SUBSTITUTED and no kind is
// inferred from the findings, because an undeclared scope is a true statement
// about a run whose caller could not enumerate its checks, and a substituted
// default would be a fabricated coverage claim.
func normalizeCheckKinds(kinds []string) []string {
	if len(kinds) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
