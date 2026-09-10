// SPDX-License-Identifier: Apache-2.0

// files_scope.go — the FILE-LIST scope: parse it strictly, resolve every named
// path before the walk, and account for every one of them afterwards.
//
// WHY THIS LIVES AT THE ANALYZER RATHER THAN AT ONE CALLER. Three faces reach
// this analyzer with a scope — the MCP tool, the topology dispatcher, which
// forwards Extra verbatim and declares no parameter of its own, and
// `knowledge check run`, which declares its own flags and then hands them to
// the MCP tool ON THE DAEMON rather than running the analyzer in its own
// process — plus the in-process entry point. A resolve performed at one of them
// leaves the others with the silent drop this scope exists to close, which is
// the same reasoning parseIncludeTests states for itself in scan.go.
//
// THE WHOLE POINT IS THAT NO NAMED PATH GOES MISSING. A file the caller names is
// SCANNED, DISCLOSED BY NAME, or REFUSED BY NAME. Two walk filters would
// otherwise take diff files without a word — discovery's exclusion rules decline
// a generated file, and the walk's own test-file filter takes a test file — so a
// file-list scope lifts both FOR THE PATHS IT NAMES and for nothing else, on the
// check-fixture runner's idiom (corpus/fixture_run.go): the caller named the
// artifact, so discovery's guess about it does not get to win.

package corpusscan

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// fileScope is a resolved file-list scope: the paths as the caller wrote them,
// plus what resolving each one against the tree established.
type fileScope struct {
	// named is every distinct path the caller supplied, verbatim and in caller
	// order. It is what the walk's package_prefixes is built from — INCLUDING
	// the paths of other languages, because handing the walk a shorter list
	// would make an all-other-language scope look like no narrowing at all,
	// which ast.Scope reads as the whole repository.
	named []string
	// langFiles is every named path that is a FILE of the corpus language — the
	// files the walk must open.
	//
	// IT IS THE PATHS AND NOT A COUNT because the expectation is now PER CHECK: a
	// check's own path scope legitimately excludes some of them, so the shortfall
	// backstop has to count the ones inside that check's scope rather than all of
	// them. A count could not answer which.
	langFiles []string
	// dirs is how many named paths are directories. It matters because a
	// directory contributes an unknown number of files to the walk, which is
	// what turns the post-walk accounting from an equality into a floor.
	dirs int
	// other is every named path that is not of the corpus language, kept so the
	// disclosure can name them.
	other []string
}

// parseFileList reads the file-list Extra key, strictly.
//
// IT READS THE RAW MAP VALUE for the same reason parseChecksSubset does: the
// foundation Extra helpers default on a missing, empty or unparseable value, and
// a silent default on a control that decides WHICH FILES ARE SCANNED is the
// worst place in this analyzer for one. An ABSENT key means "no file list" and
// is legitimate; a present-but-empty one is a caller mistake and is refused
// rather than read as "every file".
//
// THE SPELLING IS CHECKED HERE AND NEVER NORMALIZED. parser.MatchesPathPrefixes
// compares at path-SEGMENT boundaries against the walk's own repo-relative
// paths, so "./x" does not match "x": a caller who wrote the first would
// otherwise get a clean verdict over a file that was never scanned. Rewriting it
// for them would be a coercion of bad input, which this repository forbids.
func parseFileList(extra map[string]string) ([]string, error) {
	raw, present := extra[ExtraKeyFiles]
	if !present {
		return nil, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("topology/%s: %s is present but empty — omit the key to scan the whole repo, or name at least one repo-relative path",
			AnalyzerName, ExtraKeyFiles)
	}
	var files []string
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, fmt.Errorf("topology/%s: %s=%q does not decode — the value is a JSON array of repo-relative paths, "+
			"which is what lets it carry every byte a path can hold (a comma-separated list would split a legitimate path in two): %w",
			AnalyzerName, ExtraKeyFiles, raw, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("topology/%s: %s is present but empty — omit the key to scan the whole repo, or name at least one repo-relative path",
			AnalyzerName, ExtraKeyFiles)
	}
	out := make([]string, 0, len(files))
	seen := map[string]bool{}
	for i, p := range files {
		if err := checkFilePathSpelling(p, i+1); err != nil {
			return nil, err
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// checkFilePathSpelling refuses a path the walk could not match, naming the
// offending value and the accepted spelling.
//
// EVERY ARM NAMES THE VALUE. A path is caller data and the caller is the only
// one who can fix it, so "one of your paths is wrong" is not an answer.
func checkFilePathSpelling(p string, position int) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("topology/%s: %s element %d is empty — every element is a repo-relative path",
			AnalyzerName, ExtraKeyFiles, position)
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return fmt.Errorf("topology/%s: %s element %d is %q, an absolute path — the walk addresses files repo-relatively, so name the path as it appears under the repo root",
			AnalyzerName, ExtraKeyFiles, position, p)
	}
	if slices.Contains(strings.Split(p, "/"), "..") {
		return fmt.Errorf("topology/%s: %s element %d is %q, which escapes the repo with a %q segment — a scan never reads outside the tree it was pointed at",
			AnalyzerName, ExtraKeyFiles, position, p, "..")
	}
	// path.Clean, not filepath.Clean: the walk's own paths are slash-separated
	// repo-relative strings on every platform, and comparing against the OS
	// separator would admit a spelling on one platform and refuse it on another.
	if cleaned := path.Clean(p); cleaned != p {
		return fmt.Errorf("topology/%s: %s element %d is %q, which is not the spelling the walk compares against — it matches paths at whole path SEGMENTS, so a leading %q, a trailing slash or a doubled separator matches nothing. The accepted spelling is %q",
			AnalyzerName, ExtraKeyFiles, position, p, "./", cleaned)
	}
	return nil
}

// resolveFileScope resolves every named path against the tree BEFORE the walk,
// and classifies it.
//
// THIS IS THE ONLY PLACE THE NAME STILL EXISTS. ast.WalkStats carries an integer
// for every count a post-walk shortfall could read and one path list —
// ExcludedSamples — which cannot serve here on three counts: it is a BOUNDED
// sample, it is keyed by DISCOVERY RULE so it names nothing the walk's own
// language, prefix or test-file filters dropped, and a file-list run lifts those
// rules so it is empty by construction on exactly these runs. The class this
// guard exists for — a named file that exists and cannot be READ — lands in
// SkippedRead, a count with no sample at all. So a post-walk check can report
// HOW MANY were not opened and never WHICH, and this is where WHICH is known.
func resolveFileScope(root, language string, files []string) (*fileScope, error) {
	scope := &fileScope{named: files}
	for _, p := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		info, err := os.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("topology/%s: %s names %q, which does not resolve under the repo root %q: %w — a scan cannot report a file it never found, so this run is refused rather than reported clean over it",
				AnalyzerName, ExtraKeyFiles, p, root, err)
		}
		if info.IsDir() {
			scope.dirs++
			continue
		}
		f, err := os.Open(full)
		if err != nil {
			return nil, fmt.Errorf("topology/%s: %s names %q, which exists and cannot be opened: %w — the walk would count it as unread without naming it, so this run is refused rather than reported clean over it",
				AnalyzerName, ExtraKeyFiles, p, err)
		}
		_ = f.Close()
		if string(treesitter.DetectLanguage(p)) != language {
			scope.other = append(scope.other, p)
			continue
		}
		scope.langFiles = append(scope.langFiles, p)
	}
	return scope, nil
}

// expectedUnder is how many named files of the corpus language fall inside a
// check's effective narrowing — the number THAT check's walk must open.
//
// A NIL OR EMPTY NARROWING IS EVERY NAMED FILE, which is the unscoped check's
// expectation and the figure this backstop used before checks could narrow.
// parser.MatchesPathPrefixes reads an empty prefix list as "matches everything",
// so the arithmetic needs no special case; it is stated here because a reader
// checking the zero-scope arm should not have to go and read the predicate.
func (s *fileScope) expectedUnder(prefixes []string) int {
	if s == nil {
		return 0
	}
	n := 0
	for _, p := range s.langFiles {
		if parser.MatchesPathPrefixes(p, prefixes) {
			n++
		}
	}
	return n
}

// namedTestFiles lists the named paths this language's own test-file convention
// claims, so an explicit include_tests=false alongside one of them is refused as
// the contradiction it is rather than resolved in one direction.
func namedTestFiles(language string, scope *fileScope) []string {
	lang := treesitter.Language(language)
	if !ast.HasTestFilePredicate(lang) {
		return nil
	}
	var out []string
	for _, p := range scope.named {
		if ast.IsTestFile(lang, p) {
			out = append(out, p)
		}
	}
	return out
}

// fileScopeShortfall is the POST-WALK backstop: the walk opened fewer files than
// the caller named, so at least one named file was never read.
//
// IT REPORTS A COUNT AND NEVER A PATH, honestly, because the stats carry no path
// for this class. The pre-walk resolve above is what names a file; this catches
// what changed between the two — a file that became unreadable, or one whose
// spelling resolves through the filesystem and not through the walk's own
// byte-comparing prefix predicate, which is what a case-insensitive filesystem
// produces.
//
// THE EXPECTATION IS THE CALLER'S PARAMETER, NOT THE SCOPE'S OWN FIELD, because
// a check's declared path scope legitimately excludes named files: comparing
// every check against the whole named list would refuse a correctly narrowed run
// as a silent drop, which is the opposite of what this backstop is for.
//
// IT IS A FLOOR RATHER THAN AN EQUALITY WHEN A DIRECTORY WAS NAMED, and that is
// stated rather than hidden: a directory is a prefix and contributes an unknown
// number of files to the walk, so a surplus proves nothing and only a shortfall
// is a defect.
func fileScopeShortfall(scope *fileScope, expected int, checkID string, stats ast.WalkStats) error {
	if scope == nil || stats.FilesScanned >= expected {
		return nil
	}
	return fmt.Errorf("topology/%s: check %q opened %d of the %d named file(s) of this corpus's language "+
		"(files_skipped=%d, of which unreadable=%d, parse errors=%d, over the parse limit=%d) — "+
		"a named file this run never opened cannot be reported clean, so the run is refused",
		AnalyzerName, checkID, stats.FilesScanned, expected,
		stats.FilesSkipped, stats.SkippedRead, stats.SkippedParseError, stats.SkippedParseLimit)
}

// otherLanguageDisclosure names the paths a file-list run named that are not of
// the corpus language.
//
// THEY ARE DROPPED RATHER THAN REFUSED, and that is a deliberate asymmetry with
// every other arm here: a real diff carries markdown, configuration and another
// language's source, while a checks corpus is ONE language by construction, so
// refusing the run would make the scope unusable for the diffs it exists for.
// What is not negotiable is that they are NAMED — a path absent from both the
// walk and the report is the silent drop this whole scope closes.
//
// IT IS A LEAD FINDING and it has an arm in ClassifyRun. A title that fold does
// not recognize counts as a flagged site, which would make every mixed diff read
// FLAGGED with a non-zero exit.
func otherLanguageDisclosure(language string, other []string) foundation.Finding {
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityInfo,
		Title:     DisclosureTitleOtherLanguage,
		Summary: fmt.Sprintf("%d named path(s) are not %s source and were not scanned: a checks corpus is one language, "+
			"and a diff is not. They are named here so no path you supplied is absent from this report.",
			len(other), language),
		Evidence: other,
		Metrics:  map[string]float64{"other_language_paths": float64(len(other))},
	}
}

// graphNotRunDisclosure records that a graph-shaped check had no candidate node
// inside the file-list scope.
//
// IT IS A DISCLOSURE AND NOT AN ERROR, which is the one place the empty-candidate
// control has to learn the difference. That control exists because an uncollected
// graph would otherwise read as a clean scan — but under a ten-file scope a
// legitimately-narrowed graph check will routinely have zero candidates, and that
// is the scope working. The uncollected-graph and wrong-node-type cases keep the
// error: they are told apart by whether the check had candidates BEFORE the
// narrowing.
func graphNotRunDisclosure(c string, nodeType string, scope *fileScope) foundation.Finding {
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityInfo,
		Title:     DisclosurePrefixGraphNotRun + c,
		Summary: fmt.Sprintf("check %q reads %q nodes from the code graph and none of them is in the %d named file(s), "+
			"so it did NOT run for this scope. It is recorded rather than folded into a clean verdict: a check that "+
			"answered nothing has not established that the named files are clean.", c, nodeType, len(scope.named)),
		Evidence: []string{c},
		Metadata: map[string]string{MetaKeyCheckID: c},
	}
}
