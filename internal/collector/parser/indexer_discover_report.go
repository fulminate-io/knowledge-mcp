// SPDX-License-Identifier: Apache-2.0

// indexer_discover_report.go — the discovery entry point that reports what it
// declined, and under which rule.
//
// Discovery drops files for eight distinct reasons and, in DiscoverFiles' form,
// says nothing about any of them: a caller who scopes a query at a declined file
// gets a zero indistinguishable from a genuine absence of matches. The reporting
// form returns the same included set plus an attribution of every declined path
// to exactly one rule, so "excluded" and "not there" stop rendering identically.
//
// Two facts make the report readable rather than merely present:
//
//   - The rule set is PER DISCOVERY PATH. discoverWithGit and discoverWithWalk
//     do not run the same chain, so the report seeds a zero for every rule the
//     path that actually ran can produce, and omits the rules it cannot. A zero
//     therefore means "this rule ran and declined nothing"; an ABSENT key means
//     "this rule never executed". DiscoveryPath names which path produced the
//     report, so the distinction is legible without inferring it from the keys.
//   - Sample name lists are CAPPED while counts are exact. On a large tree one
//     rule can decline hundreds of paths, and an unbounded name list would be
//     the largest thing in the response. Each rule therefore carries a
//     truncation flag, so "three declined" is distinguishable from "three shown
//     of nine hundred".

package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

// The eight exclusion rules, spelled exactly as the walk-exclusion census and
// its frozen baseline key them. These names are a reporting contract: they are
// compared against an independently-measured census, so a rename here without
// one there turns an agreement check into a silent disagreement.
const (
	RuleExtension     = "skip_extension"
	RuleLockfile      = "skip_lockfile"
	RuleDTS           = "skip_dts"
	RuleGeneratedGo   = "skip_generated_go"
	RulePathComponent = "skip_path_component"
	RuleUnknownLang   = "skip_unknown_lang"
	RuleTooLarge      = "skip_too_large"
	RuleDir           = "skip_dir"
)

// DiscoveryPathGit and DiscoveryPathWalk name the two discovery paths, in the
// census's own spellings so an engine tally and the frozen baseline are directly
// comparable. A lifted run appends discoveryLifted: nothing was declined because
// nothing was ALLOWED to decline, which is a different fact from a tree that had
// nothing to decline, and the two must not render identically.
const (
	DiscoveryPathGit  = "git"
	DiscoveryPathWalk = "nongit"
	discoveryLifted   = "+lifted"
)

// maxExclusionSamples bounds the NAMES reported per rule. Counts are exact and
// unbounded; only this list is capped.
const maxExclusionSamples = 5

// gitPathRules are the rules discoverWithGit can produce. skip_dir is absent
// because skipDirs is consulted only by the filesystem walk — git ls-files never
// offers a path under one.
var gitPathRules = []string{
	RuleExtension, RuleLockfile, RuleDTS, RuleGeneratedGo,
	RulePathComponent, RuleUnknownLang, RuleTooLarge,
}

// walkPathRules are the rules discoverWithWalk can produce: every git-path rule
// plus skip_dir. skip_path_component stays in the set because isIndexable still
// evaluates it on this path, but it reads zero in practice — skipDirs prunes the
// same directory names first, and a rule pre-empted by an earlier one is a
// truthful zero rather than a rule that never ran.
var walkPathRules = append([]string{RuleDir}, gitPathRules...)

// DiscoveryOptions carries the caller's discovery-time choices.
type DiscoveryOptions struct {
	// PackagePrefixes restricts discovery to paths at or under any of these
	// repo-relative prefixes, matched at PATH-SEGMENT boundaries: "pkg" admits
	// "pkg" and everything under "pkg/", and never the sibling "pkgextra". Empty
	// means the whole tree.
	//
	// This is a narrowing the caller asked for, not a rule discovery applied, so
	// paths outside the prefixes are absent from the exclusion report entirely
	// rather than counted under a rule. A consequence worth knowing when
	// comparing two reports: a scoped run tallies only what it discovered, so its
	// per-rule counts are a slice of an unscoped run's, not the same numbers.
	PackagePrefixes []string
	// LiftExclusions walks the declined set instead of declining it: every rule
	// above is bypassed. It does NOT lift .gitignore — that is git ls-files' own
	// filtering and belongs to the repo's configuration rather than to a rule
	// discovery chose — and it does not lift any filter a caller applies to the
	// returned set afterward.
	LiftExclusions bool
}

// DiscoveryReport attributes everything a discovery pass declined to the rule
// that declined it.
type DiscoveryReport struct {
	// DiscoveryPath is the path that actually ran: DiscoveryPathGit or
	// DiscoveryPathWalk, suffixed with discoveryLifted when exclusions were
	// lifted.
	DiscoveryPath string
	// ExcludedByRule is the exact count per rule, seeded to zero for every rule
	// the running path can produce.
	ExcludedByRule map[string]int
	// ExcludedSamples names up to maxExclusionSamples declined paths per rule.
	// Nil for a rule that declined nothing.
	ExcludedSamples map[string][]string
	// ExcludedTruncated is true for a rule whose declined set outran the sample
	// cap, so a short sample list is never mistaken for a short decline list.
	ExcludedTruncated map[string]bool
}

// newDiscoveryReport seeds a report for one discovery path: a zero count and a
// false truncation flag for every rule that path can produce, and nothing for
// the rules it cannot.
func newDiscoveryReport(path string, rules []string, opts DiscoveryOptions) DiscoveryReport {
	if opts.LiftExclusions {
		path += discoveryLifted
	}
	rep := DiscoveryReport{
		DiscoveryPath:     path,
		ExcludedByRule:    make(map[string]int, len(rules)),
		ExcludedSamples:   make(map[string][]string, len(rules)),
		ExcludedTruncated: make(map[string]bool, len(rules)),
	}
	for _, r := range rules {
		rep.ExcludedByRule[r] = 0
		rep.ExcludedTruncated[r] = false
	}
	return rep
}

// record charges one declined path to its rule. The count always moves; the name
// is kept only while the per-rule sample budget lasts, after which the rule is
// marked truncated.
func (r *DiscoveryReport) record(rule, rel string) {
	r.ExcludedByRule[rule]++
	if len(r.ExcludedSamples[rule]) < maxExclusionSamples {
		r.ExcludedSamples[rule] = append(r.ExcludedSamples[rule], rel)
		return
	}
	r.ExcludedTruncated[rule] = true
}

// normalizePrefix strips trailing separators so "lib" and "lib/" mean the same
// directory. Both spellings are in live use — the tool's own callers write
// either — and a segment-boundary test that appended its own separator would
// turn "lib/" into the unmatchable "lib//" and silently return nothing.
func normalizePrefix(p string) string {
	return strings.TrimRight(p, "/")
}

// MatchesPathPrefixes reports whether a repo-relative path lies at or under any
// prefix, at path-segment boundaries: "a/b" admits a/b and everything under
// a/b/, and never a/bc. A prefix naming a single FILE matches that file exactly.
// No prefixes means no restriction.
//
// This is the in-process counterpart of the git pathspecs discoverWithGit hands
// to `git ls-files`, and the two must agree: the pathspec decides what the git
// path ever sees, this predicate decides what the walk keeps, and a discovery
// that answered differently depending on which path ran would make a scoped
// result depend on whether the tree happened to be a git repo.
//
// It is EXPORTED because the ast engine applies the same caller-supplied
// prefixes to what discovery returns, and two implementations of one boundary
// rule is how they drift: ast once carried a bare strings.HasPrefix, which let a
// scope of "pkg" admit pkgextra and widened a blast-radius control on its write
// path. There is now one definition and the ast side calls it.
func MatchesPathPrefixes(rel string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, raw := range prefixes {
		p := normalizePrefix(raw)
		if p == "" || rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// dirCanContainPrefix reports whether a directory could hold any path the
// prefixes admit — true when the directory sits inside a prefix (everything
// under it qualifies) or a prefix reaches deeper into it (something under it may
// qualify). False means the whole subtree is out of scope and the walk can prune
// it unread, which is the entire saving this offers on the non-git path.
func dirCanContainPrefix(relDir string, prefixes []string) bool {
	if len(prefixes) == 0 || relDir == "." || relDir == "" {
		return true
	}
	for _, raw := range prefixes {
		p := normalizePrefix(raw)
		if p == "" || relDir == p || strings.HasPrefix(relDir, p+"/") || strings.HasPrefix(p, relDir+"/") {
			return true
		}
	}
	return false
}

// pathspecsFor renders prefixes as git pathspecs. The literal magic disables
// wildcard interpretation — a package prefix is a path, and a caller with a
// bracket or asterisk in a directory name should get that directory rather than
// a glob — while leading-directory matching, which is the segment-boundary
// behavior this relies on, is unaffected by it.
func pathspecsFor(prefixes []string) []string {
	if len(prefixes) == 0 {
		return nil
	}
	// Cap on len(prefixes) alone; the "--" leader append grows for free.
	// Computing the size as len(prefixes)+1 would be an addition the allocator
	// sees, which can overflow int for a pathologically large slice (CWE-190),
	// and a bare len() cannot.
	specs := make([]string, 0, len(prefixes))
	specs = append(specs, "--")
	for _, raw := range prefixes {
		p := normalizePrefix(raw)
		if p == "" {
			// A prefix that normalizes away means the repo root, which is no
			// restriction at all — and a pathspec list is a UNION, so emitting
			// nothing here while keeping the others would narrow rather than
			// widen. Drop the pathspecs entirely instead.
			return nil
		}
		specs = append(specs, ":(literal)"+p)
	}
	return specs
}

// DiscoverFilesReporting returns the same included paths as DiscoverFiles plus
// the exclusion report behind them. It adds no filesystem work: the
// classification is a branch on data isIndexable already had in hand, and the
// one os.Stat the size rule needs was already being paid.
//
// The report describes the path that ACTUALLY RAN. When git ls-files fails and
// discovery falls back to the filesystem walk, the git-path report is discarded
// rather than merged — a report blending the two would name rules from a path
// that produced none of the returned files.
func DiscoverFilesReporting(ctx context.Context, repoDir string, opts DiscoveryOptions) ([]string, DiscoveryReport, error) {
	rep := newDiscoveryReport(DiscoveryPathGit, gitPathRules, opts)
	files, err := discoverWithGit(ctx, repoDir, opts, &rep)
	if err != nil {
		// git_stderr carries the REASON rather than leaving the reader with a bare
		// exit status. Emitted unconditionally, empty when git wrote nothing, so
		// "git said nothing" stays distinguishable from "nobody logged it". The
		// level stays WARN deliberately: the two discovery paths run genuinely
		// different rule chains, so which one served a result is something a
		// reader wants told, not a detail to quieten.
		slog.Warn("git ls-files failed, falling back to filesystem walk",
			"error", err, "git_stderr", gitStderrLine(err))
		rep = newDiscoveryReport(DiscoveryPathWalk, walkPathRules, opts)
		files, err = discoverWithWalk(repoDir, opts, &rep)
		return files, rep, err
	}
	return files, rep, nil
}

// DiscoveryFingerprint is a canonical digest of the discovery CONFIGURATION a
// collect ran under: which discovery path actually executed (git vs filesystem
// walk, and whether exclusions were lifted), plus the caller's package-prefix
// scoping and lift choice.
//
// WHY IT EXISTS. A scoped or differently-configured collect emits nothing for the
// files it did not visit, and a scoped-out file leaves NO trace in the result —
// so downstream there is nothing to derive the configuration from. Without this
// value a collect scoped by package prefixes would name every out-of-scope path
// as deleted, and every deletion guard would admit it: the walk was complete
// (nothing was unreadable), the ratio is ordinary for a subtree, and each named
// path really does have a live collector-owned node. This is the one place a
// legitimate user action could otherwise destroy data.
//
// DETERMINISM IS THE WHOLE PROPERTY. It digests only VALUES the caller chose and
// the path that ran — never a timestamp, never an absolute path, never a map in
// iteration order. Prefixes are sorted before digesting. A fingerprint that
// varied run-to-run would differ on every collect, trip the discovery-change
// trigger every time, and leave the incremental diff PERMANENTLY disarmed for
// every repository — a quiet death with every gate still green, which is why the
// determinism has a dedicated test rather than only a comment.
func DiscoveryFingerprint(discoveryPath string, opts DiscoveryOptions) string {
	prefixes := append([]string(nil), opts.PackagePrefixes...)
	sort.Strings(prefixes)
	var b strings.Builder
	b.WriteString("path=")
	b.WriteString(discoveryPath)
	b.WriteString("\nlift=")
	b.WriteString(strconv.FormatBool(opts.LiftExclusions))
	// LENGTH-PREFIXED, NOT DELIMITER-JOINED. Any join is ambiguous: a single
	// prefix that happens to contain the delimiter digests identically to the two
	// prefixes it looks like, so two different configurations would compare equal
	// and a scoping change between them would go undetected. Writing the count and
	// then each prefix's byte length makes the encoding injective regardless of
	// what a path contains.
	b.WriteString("\nprefixes=")
	b.WriteString(strconv.Itoa(len(prefixes)))
	for _, p := range prefixes {
		b.WriteString(":")
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteString(":")
		b.WriteString(p)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
