// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"path/filepath"
	"strings"
	"sync"
)

// RefSite is the resolution context of every reference a declaration emits.
// It is built ONCE PER FILE in ChunkFile and the same pointer is assigned to
// every reference-carrying edge that file produces — one pointer word per
// edge, one Binds map per file, never a copy.
//
// Before this type existed, ChunkContext.Imports was captured per file
// (chunker_filecontext.go:19-37) and then discarded: nothing downstream of
// the chunker could see what a file imported, so a reference could only ever
// be matched by name. RefSite is where that discard stops.
type RefSite struct {
	// File is the reference's own file path — the first component of the
	// ambiguity group key and the anchor for file-order assertions.
	File string

	// Scope is the language's resolution unit for this file, as returned by
	// ScopeID: a directory for Go, a declared namespace for C#/PHP, the file
	// itself for every other registered language.
	Scope string

	// Parent is the container name of the declaration that emitted the
	// reference — the receiver type for a Go method, the class for a member —
	// or "" for a top-level declaration.
	Parent string

	// Lang is the file's language. It selects the BindsResolver arm and lets a
	// per-language rule read the language without re-deriving it from the path.
	Lang Language

	// Binds maps an unqualified or qualified reference NAME onto the bind its
	// import establishes. nil for every language with no registered
	// BindsResolver, which is all of them until a dependent ticket registers
	// one — and a nil-map read yields the zero Bind with ok false, which is
	// exactly what the two import rules test.
	//
	// The map is ALLOCATED by the chunker when an arm is registered and FILLED
	// IN PLACE by the parser's post-chunk pass. It is never reassigned: a
	// parented site is a by-value copy of the file-level one, so an assignment
	// would update the file-level site alone and leave every parented
	// reference reading a stale header.
	Binds map[string]Bind

	// DotScopes holds the scope IDs whose exported names join this file's
	// unqualified namespace — Go's dot import being the case it exists for. It
	// is nil unless the file's language has a registered arm AND that arm
	// reported dot scopes for the file.
	//
	// IT IS A MAP AND NOT A SLICE, AND THAT IS LOAD-BEARING. It takes the same
	// allocate-then-fill-in-place discipline Binds documents directly above,
	// for the same reason: a parented site is a by-value copy, so a field the
	// post-chunk pass ASSIGNS is invisible to every parented copy. A map is a
	// reference type and can be filled through a copy; a slice cannot, because
	// appending to a nil slice cannot be made visible through one.
	//
	// A NAME FOUND IN SEVERAL OF THESE SCOPES FORMS A GROUP, NEVER A WINNER.
	// At package level Go FORBIDS a dot-import collision rather than resolving
	// it — a local declaration colliding with a dot-imported name, or two dot
	// imports exporting one name, are both declaration-time errors — so there
	// is no shadowing rule to encode and no precedence to read into this set.
	DotScopes map[string]bool

	// QualifierTypes maps a qualifier NAME visible inside one declaration —
	// a receiver, a parameter, a local variable — onto the type it was
	// declared with, for the typed-qualifier resolution rung. It is nil for
	// every language with no registered qualifier-type arm, and nil is the
	// whole of the unregistered contract: the rung reads a nil map as "no
	// candidate" and falls through unchanged.
	//
	// THIS FIELD IS PER-DECLARATION, WHICH EVERY OTHER FIELD ON THIS STRUCT IS
	// NOT, AND THE DIFFERENCE IS LOAD-BEARING. Binds and DotScopes are
	// per-FILE maps ALLOCATED by the chunker and FILLED IN PLACE by the
	// parser's post-chunk binds pass; a by-value copy shares their map
	// headers, which is exactly why that fill is visible through every
	// parented copy. QualifierTypes is the opposite: it is built PER
	// DECLARATION during chunking and never filled later, so it must be
	// ASSIGNED onto a site no other declaration shares. refForDeclaration is
	// what guarantees that — a declaration carrying qualifier types always
	// gets its own site copy rather than the shared file-level pointer.
	//
	// Do NOT apply the allocate-then-fill-in-place discipline the two fields
	// above document to this one. Allocating it per file and filling it per
	// declaration would leak every declaration's locals into every other
	// declaration in the same file, which is precisely the aliasing defect
	// the per-declaration copy exists to prevent.
	QualifierTypes map[string]QualType
}

// QualType is the type one qualifier name was declared with, as the chunker
// read it out of the declaration's own syntax.
//
// It carries the type AS WRITTEN rather than a resolved target: the chunker
// has no declaration index, so binding the text to a declaration is the
// parser's job on the resolution ladder. Everything here is what syntax alone
// can establish.
type QualType struct {
	// Text is the type or callee expression AS WRITTEN, with pointer stars,
	// parens and type arguments stripped: "T" or "pkg.T".
	Text string

	// FromCall reports that Text names a function or method whose declared
	// RESULT type is the qualifier's type, rather than naming the type
	// directly. The parser needs the extra hop to reach the type.
	FromCall bool

	// ResultIndex is the 0-based position in that callee's result list, and is
	// 0 for the single-result case. It is read only when FromCall is true.
	ResultIndex int
}

// ScopeKind is the resolution unit a language uses: the granularity at which
// two declarations are visible to one another without qualification.
type ScopeKind int

const (
	// ScopeFile — a declaration is visible only within its own file.
	ScopeFile ScopeKind = iota
	// ScopeDir — a declaration is visible to every file in its directory.
	ScopeDir
	// ScopeDeclaredNamespace — a declaration is visible to every file
	// declaring the same namespace, wherever those files live.
	ScopeDeclaredNamespace
	// ScopeModule — a declaration is visible to every file in the same BUILD
	// MODULE, which the source tree's layout convention identifies rather than
	// any clause in the file. APPENDED, NEVER INSERTED: ScopeFile must stay the
	// iota ZERO VALUE, because the closed table below is written out explicitly
	// precisely so a missing entry fails loudly instead of taking the zero
	// value silently.
	ScopeModule
)

// scopeKinds is a CLOSED table: an entry is required for every language in the
// registry. ScopeFile is the zero value of ScopeKind, which is exactly why
// every entry is written out explicitly and why the resolution matrix asserts
// one entry per registered language — a language added to the registry and
// forgotten here would take the default silently instead of failing loudly.
//
// Assigning a NARROWER-than-real unit can only widen the residue; it can never
// mis-bind. A file-scoped assignment for a language that is really
// module-scoped leaves cross-file references unbound rather than bound to the
// wrong declaration.
var scopeKinds = map[Language]ScopeKind{
	LangGo: ScopeDir,

	// THE FIVE DECLARED-NAMESPACE LANGUAGES. C# and PHP declare a NAMESPACE;
	// java, kotlin and scala declare a PACKAGE, which is the same unit under
	// another spelling — two files in different directories that declare it are
	// in one resolution unit, and a fully-qualified reference names it directly.
	//
	// THIS ENTRY AND declaredFileNamespace's PACKAGE-CLAUSE ARM ARE ONE CHANGE
	// AND MUST NOT BE SEPARATED. ScopeID reads a declared namespace ONLY under
	// this kind, so the reader without this entry is inert — and this entry
	// without the reader is WORSE than inert, because ScopeID's fallback would
	// silently put every java, kotlin and scala file on a directory scope,
	// changing the meaning of every existing resolution with no gate to catch it.
	//
	// THE NARROW MISS declaredNamespaceBinds DOCUMENTS NOW COVERS FIVE LANGUAGES
	// RATHER THAN TWO: ScopeID falls back to a directory scope when a file's
	// declared namespace equals its directory-derived one, so a single-segment
	// `package foo` inside a directory named foo is indexed under a dir scope
	// while an import arm builds a namespace one. The two disagree, the index
	// reports the scope empty, and the reference TERMINATES — a missing edge,
	// never a wrong one. Do not add a directory probe to paper over it.
	LangCSharp: ScopeDeclaredNamespace,
	LangPHP:    ScopeDeclaredNamespace,
	LangJava:   ScopeDeclaredNamespace,
	LangKotlin: ScopeDeclaredNamespace,
	LangScala:  ScopeDeclaredNamespace,

	// SWIFT IS MODULE-SCOPED, and it is the one language here whose unit no
	// clause in the file declares: two files of one module see each other with
	// no import at all, so the unit has to come from the source tree's layout.
	// THIS ENTRY AND ScopeID's ScopeModule ARM ARE ONE CHANGE AND MUST NOT BE
	// SEPARATED, for the reason the declared-namespace note above gives.
	LangSwift: ScopeModule,

	LangTypeScript: ScopeFile,
	LangTSX:        ScopeFile,
	LangPython:     ScopeFile,
	LangRust:       ScopeFile,
	LangC:          ScopeFile,
	LangCPP:        ScopeFile,
	LangJavaScript: ScopeFile,
	LangRuby:       ScopeFile,
	LangElixir:     ScopeFile,
	LangLua:        ScopeFile,
	LangBash:       ScopeFile,
	LangGroovy:     ScopeFile,
	LangElm:        ScopeFile,
	LangOCaml:      ScopeFile,
	LangHCL:        ScopeFile,
	LangProtobuf:   ScopeFile,
	LangCSS:        ScopeFile,
	LangHTML:       ScopeFile,
	LangSQL:        ScopeFile,
	LangDockerfile: ScopeFile,
	LangSvelte:     ScopeFile,
	LangToml:       ScopeFile,
	LangYaml:       ScopeFile,
	LangMarkdown:   ScopeFile,
	LangCue:        ScopeFile,
}

// ScopeID returns the resolution-unit identifier for one file, prefixed by the
// kind so two units of different kinds can never collide on the same string.
//
// declaredNS receives the value the chunker already computes: extractFileContext
// overwrites ctx.PackageName with declaredFileNamespace's return for PHP and
// C#. It is read ONLY under ScopeDeclaredNamespace and only when it differs
// from fileNamespace(filePath, lang) — the derived default — so a PHP file that
// declares no sibling-form namespace falls back to its directory rather than
// claiming a namespace it never declared.
func ScopeID(filePath string, lang Language, declaredNS string) string {
	switch scopeKinds[lang] {
	case ScopeDir:
		return "dir:" + filepath.Dir(filePath)
	case ScopeDeclaredNamespace:
		if declaredNS != "" && declaredNS != fileNamespace(filePath, lang) {
			return "ns:" + declaredNS
		}
		return "dir:" + filepath.Dir(filePath)
	case ScopeModule:
		if mod := moduleScopeFor(filePath, lang); mod != "" {
			return mod
		}
		// A PATH OUTSIDE THE LAYOUT CONVENTION KEEPS TODAY'S EXACT ANSWER. The
		// narrow unit is the safe one, so a tree the derivation cannot read is
		// left file-scoped rather than guessed at.
		return "file:" + filePath
	default:
		return "file:" + filePath
	}
}

// moduleScopeFor dispatches one language's module derivation, or PANICS.
//
// THE PANIC IS THE POINT. A language given ScopeModule with no derivation arm
// is a programming error, and silently taking the file-scope fallback is
// exactly the "takes the default silently instead of failing loudly" failure
// the closed table above exists to prevent. It cannot fire in steady state:
// the table and this switch are edited together.
func moduleScopeFor(filePath string, lang Language) string {
	switch lang {
	case LangSwift:
		return swiftModuleScope(filePath)
	default:
		panic("treesitter: moduleScopeFor(" + string(lang) + "): the language is module-scoped but declares no module derivation")
	}
}

// swiftModuleScope derives a swift file's module from the package layout
// convention: a module's sources live under `Sources/<Module>/`, a test
// target's under `Tests/<Module>/`. It returns "" for any path that does not
// fit, which is the caller's signal to fall back to file scope.
//
// Three properties, each chosen against a specific failure:
//
//   - THE KEY IS THE FULL PATH PREFIX, NEVER THE BARE MODULE NAME. A tree with
//     `pkgA/Sources/Utils/` and `pkgB/Sources/Utils/` holds two DIFFERENT
//     modules that merely share a target name; keying on the name alone would
//     merge them and mis-bind across package boundaries.
//   - THE LAST `Sources`/`Tests` SEGMENT WINS, so a package nested inside
//     another package's tree resolves to the inner one.
//   - `Tests/<Name>/` IS ITS OWN MODULE: a test target is a separate module and
//     its declarations are genuinely not visible to the library target.
//
// THE HONEST RESIDUE: a package manifest may give a target a CUSTOM path, and
// an IDE-defined project names its modules in a project file with no layout
// convention at all. Those trees fall back to file scope — no change, no harm —
// except where a custom path puts two real targets under one directory, which
// over-merges them. That case is DECIDABLE by reading the manifest, which the
// collector does not do today: an incompleteness with a known fix, not a limit.
func swiftModuleScope(filePath string) string {
	segments := strings.Split(filePath, "/")
	// The last segment is the file name, so a module directory must appear
	// before it with at least one segment in between.
	for i := len(segments) - 3; i >= 0; i-- {
		if segments[i] != "Sources" && segments[i] != "Tests" {
			continue
		}
		return "mod:" + strings.Join(segments[:i+2], "/")
	}
	return ""
}

// Bind is what one import establishes for one name.
type Bind struct {
	// Scope is the ScopeID of the unit the name resolves into.
	Scope string

	// Name is the DECLARED name at the target, empty meaning "use the
	// reference's own name". It exists for renaming imports —
	// `import {A as B}`, `use x::y as z`, `from x import a as b` — where the
	// reference's own text is not what the target declares.
	//
	// The override applies to the NAME KEY of the UNQUALIFIED import rule ONLY.
	// The qualified rule must not apply it there: `import * as ns` renames the
	// MODULE, not its members, so a namespace member keeps its own spelling.
	// The qualified rule DOES read it as the PARENT key, which is a different
	// question — see resolveQualifiedImport in the parser package.
	Name string

	// Container is THE DECLARED PARENT THE BOUND NAME IS A MEMBER OF, and it is
	// EMPTY when the bound name is a top-level declaration. It exists for the
	// import forms that name a member of a type rather than the type itself —
	// java's `import static a.b.C.d`, where the bind keys "d" while the
	// declaration that satisfies it is parented to "C".
	//
	// THE ZERO VALUE IS THE WHOLE CONTRACT. Empty means "this bind names a
	// top-level declaration", which is what every bind an arm records today
	// means, so an arm that never sets it keeps its exact current behavior and
	// the resolution rungs read it as the no-op it is. That is why adding this
	// field required no edit to any other arm.
	//
	// It is READ ONLY by the two import rungs, and only to supply the Parent of
	// a second lookup. It is never compared to a scope, never used to derive a
	// path, and never written by the resolution walk.
	Container string
}

// THERE IS DELIBERATELY NO External FIELD. Externality — "this bind's target
// contributes no entry to the declaration index" — is COMPUTED from the index
// by the external-qualifier rule, never carried by an arm.
//
// An arm must already compute the target's Scope, since that is what the two
// import rules look up; externality is a property OF THAT SCOPE, and the index
// is what knows it. So the arm records where it thinks the target is and the
// index says whether anything is there — one failure mode instead of two, and
// no second predicate to keep in sync.
//
// THE ARM'S WHOLE OBLIGATION follows from that: record a bind for EVERY import
// it can attribute, carrying its best-effort Scope. OMITTING a bind for an
// import judged external is what loses the termination, because the rule cannot
// see what the arm never recorded.

// RepoContext is the repository-level information a BindsResolver needs and
// the chunker cannot supply: ChunkFile takes a repo-relative path and holds no
// repo handle at all.
//
// One struct rather than separate parameters because different arms need
// different parts — a Go arm needs ModulePath, a TypeScript arm needs Root
// plus Files — and a struct absorbs a fifth language's needs without churning
// every registered arm's signature.
// THE TWO DERIVED CACHES BELOW ARE FIELDS ON THIS STRUCT AND ARE NEVER
// PACKAGE-LEVEL STATE. The ful1347 multi-language corpus harness constructs a
// FRESH RepoContext per corpus root and measures SEVEN repositories in ONE
// process, so a package-level cache would derive the first corpus's layout and
// then serve it
// to every other corpus in the same run — silently resolving one repository's
// imports against another repository's directory layout. No unit test can catch
// that: a single-fixture test builds one RepoContext, where package-level and
// per-context caches behave identically. Scoping them to the struct makes the
// hazard unrepresentable rather than merely avoided.
//
// A RepoContext CARRYING THEM MUST BE PASSED BY POINTER, which is why
// chunkResultsToPopulate takes one: copying a struct that holds a sync
// primitive is a vet failure, and copying one that holds a cache would fork the
// cache silently.
type RepoContext struct {
	// Root is the absolute repository directory.
	Root string
	// ModulePath is the module's declared import path, where the language has
	// one.
	ModulePath string
	// Files is the DISCOVERED file set, repo-relative. Preferred over a set
	// derived from chunk results: a file that produced no chunks still has a
	// scope.
	Files []string

	// rustAnchors caches the crate-root anchors of one DIRECTORY — the answer
	// depends on the directory alone, so a corpus pays the ancestor walk once
	// per directory instead of once per file. Guarded rather than derived
	// behind a Once because it is filled incrementally, one directory at a time.
	rustAnchorMu sync.Mutex
	rustAnchors  map[string][]string

	// jvmSourceRoots is the repository-level set of directories a package-shaped
	// path hangs off, derived ONCE from the chunked file set. It is what a
	// CROSS-MODULE JVM import resolves through: rung 1 can only reach roots that
	// are ancestors of the importing file, and a test under guava-tests/test/
	// importing a type under guava/src/ has no such ancestor.
	jvmSourceRootsOnce sync.Once
	jvmSourceRoots     []string
}

// BindsResult is everything one file's imports establish: the per-name binds,
// and the scopes a wildcard-style import folds wholesale into the file's
// unqualified namespace.
//
// IT IS A STRUCT RATHER THAN A SECOND RETURN VALUE for the reason RepoContext
// is a struct: the next language will need a third thing, and a struct absorbs
// it without churning every registered arm's signature.
//
// THE ARM RETURNS A SLICE OF DOT SCOPES WHILE RefSite CARRIES A MAP, and the
// asymmetry is deliberate. The arm PRODUCES A VALUE and never touches a
// reference site — an arm that reached into self.Ref would reintroduce the
// by-value aliasing defect from the other side — while the PASS writes those
// entries into the site's already-allocated map, in place.
type BindsResult struct {
	// Binds maps a reference NAME onto the bind its import establishes.
	Binds map[string]Bind

	// DotScopes are the scope IDs whose exported names join the file's
	// unqualified namespace. Order is not read: the pass writes them into a
	// set, and the walk sorts that set itself.
	DotScopes []string
}

// BindsResolver maps a file's imports onto what they establish: the binds keyed
// by the NAMES a reference may use, plus any scopes folded in wholesale.
//
// self is the file being resolved and byPath is every other file's result,
// keyed by path — which together are what let a re-export chain resolve
// without a second parse.
type BindsResolver func(rc *RepoContext, byPath map[string]*Result, self *Result) BindsResult

// bindsResolvers holds the registered per-language arms. Populated at init by
// each language's own package — jsmodule installs the ECMAScript arm for
// typescript, tsx and javascript — and empty for every language that ships
// none, whose import rules in the resolution ladder stay inert.
var bindsResolvers = map[Language]BindsResolver{}

// RegisterBindsResolver installs the arm for one language. It is the seam the
// dependent per-language exact-binding tickets (Go, TypeScript/JavaScript, and
// the remaining-language ticket) are expected to call from an init function in
// their own chunker_<lang>.go.
//
// A registered arm changes resolution outcomes for its language THROUGH THE
// REGISTRIES AND THE PROFILE — the scope table, this resolver map, and the
// per-language resolution profile. Ladder rungs themselves are edited only
// under the sanctioned edits the plan enumerates: the import-versus-local
// precedence, the qualifier separator, and the external-qualifier rung. A
// language whose LOCAL declarations legally shadow its imports must not
// register a bare-name arm without also carrying that precedence, because the
// ladder consults the import rule before the own-scope rule; registering the
// arm alone would silently bind imports where the language binds locals.
func RegisterBindsResolver(lang Language, r BindsResolver) {
	bindsResolvers[lang] = r
}

// hasBindsResolver reports whether a language has a registered arm. The chunker
// consults it to decide whether to ALLOCATE a file's Binds and DotScopes maps:
// a language with no arm keeps both nil and pays no per-file allocation.
func hasBindsResolver(lang Language) bool {
	_, ok := bindsResolvers[lang]
	return ok
}

// UnregisterBindsResolver removes a language's arm, restoring the unregistered
// state exactly.
//
// It is what makes the seam testable without leaking: the resolution tests
// install an arm to exercise the two import rules, and an arm left registered
// afterwards would change resolution outcomes for every later test in the same
// binary — including the all-language matrix. Registering a nil arm is NOT the
// way back, because BindsFor calls whatever the map holds.
//
// The inverse hazard is just as real: unregistering a language that ships a
// PRODUCTION arm deletes that registration for every later test in the binary,
// silently disarming the feature under test. A test that installs a fake over
// a production arm must RESTORE the production registration in its cleanup
// (e.g. RegisterGoBindsResolver), never merely delete its fake.
func UnregisterBindsResolver(lang Language) {
	delete(bindsResolvers, lang)
}

// BindsFor runs the registered arm for one file's language, or returns the ZERO
// BindsResult when none is registered. Zero rather than a populated struct is
// deliberate: both of its fields stay nil, so the languages with no arm pay no
// allocation, and the pass's own emptiness check reads the whole result.
//
// It is EXPORTED because construction moved to the parser: ChunkFile takes a
// repo-relative path and holds no repo handle, so no chunker-side arm could
// ever reach the repo root, the discovered file set, or a tsconfig. The
// declarations stay here; only the call site is over there.
func BindsFor(rc *RepoContext, byPath map[string]*Result, self *Result) BindsResult {
	if self == nil {
		return BindsResult{}
	}
	r, ok := bindsResolvers[self.Language]
	if !ok {
		return BindsResult{}
	}
	return r(rc, byPath, self)
}
