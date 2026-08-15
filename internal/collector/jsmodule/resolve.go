// SPDX-License-Identifier: Apache-2.0

package jsmodule

import (
	"errors"
	"path"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// Target is the declaration one import binds to: the repo-relative file that
// declares it, and the name it is declared under there.
//
// Name is EMPTY when the reference's own name is already the declared one — a
// namespace import, whose members keep their own spelling, and an anonymous
// default export.
type Target struct {
	File string
	Name string
}

// Outcome classifies what resolution concluded. It is RESOLVER-INTERNAL
// DIAGNOSTICS and never rides a Bind: externality is answered by the
// declaration index's own scope set, not by anything an arm carries.
//
// It survives as a return value because the corpus artifact reports four
// diagnostic sub-buckets and a bool cannot carry four values.
type Outcome int

const (
	// OutcomeBound — resolved to a discovered file that declares names.
	OutcomeBound Outcome = iota
	// OutcomeNoNamedDecls — resolved to a discovered file whose chunks are all
	// unnamed. The caller records THE SAME BIND it would for OutcomeBound; the
	// file's scope simply never entered the index, which is what makes the
	// reference external without anyone deciding so.
	OutcomeNoNamedDecls
	// OutcomeUndiscovered — a relative or tsconfig-path candidate matching no
	// discovered file. The candidate path is real, so the caller still records
	// a bind scoped to it.
	OutcomeUndiscovered
	// OutcomeOutOfRepo — absolute, or bare with no in-repo workspace package.
	OutcomeOutOfRepo
	// OutcomeRefused — the kind binds no local name at all, so there is no map
	// key to record under. Only ImportWildcard and ImportSideEffect reach it.
	OutcomeRefused
)

// FileExports is what one file offers to importers, as captured during
// chunking.
type FileExports struct {
	// Declared holds the file's top-level declaration names, NON-EMPTY names
	// only. It is read for NAME RESOLUTION alone — "does this file declare the
	// name, or must a re-export be followed" — and never to decide externality.
	Declared map[string]bool

	// ReExports is the file's `export ... from` table, in declaration order.
	ReExports []treesitter.ReExport

	// DefaultName is the declared name behind the file's `export default`,
	// empty when it default-exports nothing or exports an anonymous value.
	DefaultName string
}

// tsExtOrder and jsExtOrder are FIXED so two files that both exist resolve the
// same way on every run. Resolve derives the order from the IMPORTER's own
// extension, so no caller passes a language and none can pass the wrong one.
var (
	tsExtOrder = []string{".ts", ".tsx", ".d.ts", ".js", ".jsx", ".mjs", ".cjs", ".json"}
	jsExtOrder = []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".json"}
)

// jsToTSTwin is the TypeScript extension substitution: a .ts file importing
// './x.js' means './x.ts'. A real rule of the language, not a convenience, and
// it is tried BEFORE extension inference.
var jsToTSTwin = map[string]string{
	".js":  ".ts",
	".jsx": ".tsx",
	".mjs": ".mts",
	".cjs": ".cts",
}

// Resolver answers module specifiers against one repository's DISCOVERED file
// set. It is immutable after construction and safe for concurrent reads.
type Resolver struct {
	root    string
	files   map[string]bool
	exports map[string]FileExports
	configs *configIndex
	pkgs    *pkgIndex
}

// NewResolver reads every tsconfig/jsconfig and package.json in the discovered
// set ONCE and builds the file-lookup set from the slice.
//
// It takes files as a []string because the ratified RepoContext carries it that
// way, so the arm hands rc.Files straight through and no caller pays a
// per-file conversion.
func NewResolver(root string, files []string, exports map[string]FileExports) (*Resolver, error) {
	if root == "" {
		return nil, errors.New("jsmodule: repository root is required")
	}
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[f] = true
	}
	return &Resolver{
		root:    root,
		files:   set,
		exports: exports,
		configs: newConfigIndex(root, files),
		pkgs:    newPkgIndex(root, files),
	}, nil
}

// Resolve maps one import binding onto the declaration it names.
//
// THE KIND SWITCH HAS AN EXPLICIT ARM FOR ALL FIVE KINDS AND NO DEFAULT
// FALL-THROUGH, so a kind a later ticket adds to the shared carrier cannot be
// silently mishandled here.
func (r *Resolver) Resolve(
	importer, specifier, imported string, kind treesitter.ImportKind,
) (Target, Outcome) {
	switch kind {
	case treesitter.ImportWildcard, treesitter.ImportSideEffect:
		// REFUSED BEFORE THE LADDER RUNS. Neither binds a local name, so no
		// amount of file resolution helps: there is no key for the Binds map.
		return Target{}, OutcomeRefused
	case treesitter.ImportNamed, treesitter.ImportDefault, treesitter.ImportNamespace:
	}

	candidate, outcome := r.candidateFor(importer, specifier)
	if outcome == OutcomeOutOfRepo {
		return Target{}, OutcomeOutOfRepo
	}

	file, ok := r.resolveFile(importer, candidate)
	if !ok {
		// The candidate path is real and repo-relative even though nothing was
		// discovered at it, so it is the honest best-effort target.
		return Target{File: candidate, Name: imported}, OutcomeUndiscovered
	}
	return r.resolveName(file, imported, kind)
}

// candidateFor walks the specifier ladder S1-S4, first hit winning, and returns
// the repo-relative candidate path a specifier names.
func (r *Resolver) candidateFor(importer, specifier string) (string, Outcome) {
	switch {
	case strings.HasPrefix(specifier, "./"), strings.HasPrefix(specifier, "../"):
		// S1 RELATIVE.
		return path.Clean(path.Join(path.Dir(importer), specifier)), OutcomeBound
	case strings.HasPrefix(specifier, "/"):
		// S3 ABSOLUTE — names a filesystem location, not a repo one.
		return "", OutcomeOutOfRepo
	}
	// S2 TSCONFIG PATHS, before the bare branch: an alias is not a package.
	if candidate, ok := r.aliasCandidate(importer, specifier); ok {
		return candidate, OutcomeBound
	}
	// S4 BARE — an in-repo workspace package, or node_modules.
	if candidate, ok := r.pkgs.resolve(specifier); ok {
		return candidate, OutcomeBound
	}
	return "", OutcomeOutOfRepo
}

// aliasCandidate applies the governing config's `paths` table.
//
// TYPESCRIPT'S MATCHING RULE IS FOLLOWED EXACTLY: a key with NO '*' must match
// the specifier exactly and is tried before any wildcard key; among wildcard
// keys the one with the LONGEST literal prefix before the '*' wins.
// Substitutions are tried in declared order and the first that resolves to a
// discovered file wins; when none does, the first substitution is the candidate
// so the caller still has a real path to scope a bind by.
func (r *Resolver) aliasCandidate(importer, specifier string) (string, bool) {
	cfg := r.configs.governing(importer)
	if cfg == nil || len(cfg.paths) == 0 {
		return "", false
	}

	if subs, ok := cfg.paths[specifier]; ok && len(subs) > 0 {
		return r.firstResolvable(importer, cfg, subs, ""), true
	}

	bestKey, bestStar, bestPrefix := "", "", -1
	for key := range cfg.paths {
		prefix, suffix, isWildcard := strings.Cut(key, "*")
		if !isWildcard {
			continue
		}
		if !strings.HasPrefix(specifier, prefix) || !strings.HasSuffix(specifier, suffix) {
			continue
		}
		if len(specifier) < len(prefix)+len(suffix) {
			continue
		}
		if len(prefix) > bestPrefix {
			bestKey, bestPrefix = key, len(prefix)
			bestStar = specifier[len(prefix) : len(specifier)-len(suffix)]
		}
	}
	if bestKey == "" {
		return "", false
	}
	return r.firstResolvable(importer, cfg, cfg.paths[bestKey], bestStar), true
}

// firstResolvable substitutes the wildcard remainder into each candidate in
// declared order and returns the first that resolves to a discovered file,
// falling back to the first candidate.
func (r *Resolver) firstResolvable(importer string, cfg *tsConfig, subs []string, star string) string {
	var first string
	for _, sub := range subs {
		candidate := path.Clean(path.Join(cfg.pathsBase, strings.ReplaceAll(sub, "*", star)))
		if first == "" {
			first = candidate
		}
		if _, ok := r.resolveFile(importer, candidate); ok {
			return candidate
		}
	}
	return first
}

// resolveFile applies the file rules F1-F5 to a candidate, first hit winning.
func (r *Resolver) resolveFile(importer, candidate string) (string, bool) {
	if candidate == "" {
		return "", false
	}
	// F1 EXACT.
	if r.files[candidate] {
		return candidate, true
	}
	// F2 TS EXTENSION SUBSTITUTION, before inference: a TypeScript file
	// importing './x.js' means './x.ts'.
	if twin, ok := jsToTSTwin[path.Ext(candidate)]; ok {
		swapped := strings.TrimSuffix(candidate, path.Ext(candidate)) + twin
		if r.files[swapped] {
			return swapped, true
		}
	}
	order := r.extOrder(importer)
	// F3 EXTENSION INFERENCE.
	for _, ext := range order {
		if r.files[candidate+ext] {
			return candidate + ext, true
		}
	}
	// F4 INDEX FILE.
	for _, ext := range order {
		if p := candidate + "/index" + ext; r.files[p] {
			return p, true
		}
	}
	// F5 no hit.
	return "", false
}

// extOrder picks the probe order from the IMPORTER's extension: a TypeScript
// file prefers TypeScript twins, a JavaScript file prefers JavaScript ones.
func (r *Resolver) extOrder(importer string) []string {
	switch path.Ext(importer) {
	case ".js", ".jsx", ".mjs", ".cjs":
		return jsExtOrder
	default:
		return tsExtOrder
	}
}

// declaresNames reports whether a resolved file contributes any named
// declaration, which separates OutcomeBound from OutcomeNoNamedDecls. It is a
// DIAGNOSTIC split only: the caller records the same bind either way.
func (r *Resolver) declaresNames(file string) bool {
	return len(r.exports[file].Declared) > 0
}

// boundOutcome picks between the two "we found the file" outcomes.
func (r *Resolver) boundOutcome(file string) Outcome {
	if r.declaresNames(file) {
		return OutcomeBound
	}
	return OutcomeNoNamedDecls
}
