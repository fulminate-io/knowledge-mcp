// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/jsmodule"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// corpusRootEnv points the verification at a real repository. The run is a
// MEASUREMENT INSTRUMENT rather than a gate, so it skips when unset: the
// numbers it produces depend on the corpus, and a corpus is not something CI
// can be assumed to have. The artifact it writes IS committed, and the gates
// read the artifact.
const (
	corpusRootEnv = "FUL1334_CORPUS_ROOT"
	// corpusArtifact is fixed rather than configurable: the release gates read
	// this exact path, so an override could only ever write the numbers
	// somewhere the gates do not look.
	corpusArtifact = "testdata/ful1334_corpus_verification.txt"

	// corpusArmOffEnv produces the BASELINE COLUMN: the same measurement with
	// the ECMAScript arms removed BEFORE any file is chunked, which is the only
	// point at which removing them matters — the chunker allocates a file's
	// Binds map exactly when an arm is registered at chunk time.
	//
	// A baseline is required rather than optional because the ticket's
	// acceptance is a COMPARISON: the arm-enabled dynamic-group count must be
	// strictly lower than the arm-disabled one. Comparing against the numbers
	// written into the plan would go false the first time anyone edits the
	// corpus.
	corpusArmOffEnv = "FUL1337_ARM_OFF"
	// corpusArmOffArtifact keeps the baseline out of the release artifact: the
	// gates read the arm-ENABLED numbers, and a baseline written over them
	// would report the pre-ticket picture as the shipped one.
	corpusArmOffArtifact = "testdata/ful1337_corpus_arm_off.txt"
)

// TestFUL1334CorpusVerification runs the REAL collector pipeline over a real
// repository and writes the resolution transition to the artifact the release
// decision reads.
//
// It runs the production functions in the production ORDER — DeduplicateChunks,
// then resolveSlotEdges, then the index build, then resolveEdgesWithStats — and
// deliberately does NOT sort each result's chunks, because that sort is what
// invalidates the chunk slots and it happens in populate only after resolution
// has consumed them.
func TestFUL1334CorpusVerification(t *testing.T) {
	root := os.Getenv(corpusRootEnv)
	if root == "" {
		t.Skipf("set %s to a repository path to run the live corpus verification", corpusRootEnv)
	}
	// Sanitized before it reaches discovery: an operator-supplied root is an
	// external input, and the walk below joins it with every discovered path.
	root = filepath.Clean(root)
	require.True(t, filepath.IsAbs(root), "%s must be an absolute path, got %q", corpusRootEnv, root)
	artifact := corpusArtifact
	if os.Getenv(corpusArmOffEnv) != "" {
		// BEFORE ANY CHUNKING, for the reason in corpusArmOffEnv's comment.
		// Not restored: this is a deliberate single-purpose invocation that
		// measures the pre-arm world, and it writes to its own artifact so it
		// cannot be mistaken for the shipped picture.
		for _, lang := range jsmodule.ArmedLanguages() {
			treesitter.UnregisterBindsResolver(lang)
		}
		// THE FLAG NOW COVERS FOUR TICKETS' ARMS AND ITS NAME NO LONGER SAYS
		// SO: the baseline is the ALL-ARMS-OFF picture. Leaving any family
		// registered would make its rows identical in both artifacts, so a
		// `<family>_dynamic_groups < baseline` gate would compare a number
		// against itself. Both names are kept — another ticket's surface, one
		// of them a landed gate's file name.
		treesitter.UnregisterBindsResolver(treesitter.LangGo)
		treesitter.UnregisterLanguageBindsResolvers()
		// The Go qualifier-type arm, for the identical reason: leaving it
		// registered would let the typed-qualifier rung bind in the BASELINE
		// column too, so the baseline would again be compared against itself.
		//
		// BOTH R2T ARMS COME OFF, and the second one is belt-and-braces rather
		// than strictly required TODAY. Censused at the time of writing:
		// declRec.ResultTypes has exactly one reader, inside
		// resolveTypedQualifier, and that reader is reachable only AFTER a
		// successful ref.QualifierTypes lookup — whose nil check is the rung's
		// first branch. So unregistering the qualifier-type arm alone already
		// disarms the rung completely, and the type-facts arm is inert without
		// it. That guarantee is CONTINGENT on every future reader of TypeFacts
		// sitting downstream of the same gate, which is not a property anything
		// enforces, so the second line makes the baseline unconditional instead
		// of conditional on a code shape. It also matches BenchmarkChunkFile's
		// arm_off side, which takes both arms off for the same reason.
		treesitter.UnregisterQualifierTypes(treesitter.LangGo)
		treesitter.UnregisterTypeFacts(treesitter.LangGo)
		artifact = corpusArmOffArtifact
		t.Logf("%s set: ECMAScript, Go binds, Go qualifier-type and Go type-facts arms "+
			"unregistered; writing the BASELINE column to %s",
			corpusArmOffEnv, artifact)
	}

	ctx := context.Background()
	files, err := DiscoverFiles(ctx, root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "control: discovery found no files under %s", root)

	results, _, err := ChunkFilesParallel(ctx, root, files)
	require.NoError(t, err)
	require.NotEmpty(t, results, "control: chunking produced no results")

	DeduplicateChunks(results)
	resolveSlotEdges(results)
	// IN PRODUCTION ORDER, and not present before the ECMAScript arm existed:
	// without it the binds are never filled and the two import rungs are inert,
	// so the instrument would report the pre-arm picture while the collector
	// produced the post-arm one.
	// THE MODULE PATH IS READ HERE TOO: this instrument is a SECOND construction
	// site of the repo context and does not go through Populate. Without it an
	// arm mapping import paths onto repo directories returns its zero result on
	// every file — no error, no other gate moving, go_binds_files zero. The ROOT
	// go.mod is what Populate reads in production, and a submodule's path is the
	// root path plus its directory, so one root path stays correct tree-wide.
	modulePath, _ := ReadModulePath(root)
	rc := treesitter.RepoContext{Root: root, ModulePath: modulePath, Files: files}
	fillBinds(&rc, results)

	ix := newDeclIndex(0)
	nodeIDs := map[string]bool{}
	chunkFile := map[string]string{} // chunk node ID → the file that declared it
	for _, r := range results {
		nodeIDs[r.FilePath] = true
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			id := ChunkNodeID(chunk)
			nodeIDs[id] = true
			chunkFile[id] = r.FilePath
			indexDeclaration(ix, r, chunk, id)
		}
	}

	edges, stats := resolveEdgesWithStats(results, ix, nodeIDs)

	rep := newCorpusReport()
	rep.countContainment(edges, chunkFile)
	rep.countGroups(edges)
	rep.countLanguages(results)
	rep.collided = countCollidedKeyResolutions(ix, edges)
	rep.stats = stats
	rep.ecma = ecmaScriptRows(results, ix, files)
	rep.goRows = goRows(results, ix)
	rep.siblingSkips = censusSiblingSkips(results, ix)
	rep.testCalls = censusTestCalls(results, edges)

	body := rep.render(root, len(files), len(chunkFile))
	require.NoError(t, os.WriteFile(artifact, []byte(body), 0o600))
	t.Logf("wrote %s\n%s", artifact, body)

	t.Logf("test-attributed: emitted=%d test_to_production=%d unidentifiable_residue_calls=%d by_language=%v",
		rep.testCalls.emitted, rep.testCalls.toProduction, rep.testCalls.residueCalls,
		rep.testCalls.emittedByLanguage)

	// The instrument checks its own key invariants too, so a bad run is loud
	// here rather than only at the artifact-reading gate.
	require.Positive(t, rep.sameFileContains, "control: no file-to-symbol containment at all")
	// THE KNOWN-POSITIVE FOR THE TEST-ATTRIBUTED ROWS. Both read zero when the
	// corpus has no test bodies AND when walkTestBlocks stopped emitting, and
	// the artifact cannot tell those apart on its own.
	require.Positive(t, rep.testCalls.emitted, "control: the chunker emitted no TEST_CALLS at all")
	require.Positive(t, rep.testCalls.toProduction,
		"control: no test-origin edge resolved to a non-test declaration")
	require.Zero(t, rep.crossFileContains, "cross-file CONTAINS must be zero")
	require.Zero(t, rep.uncontained, "every chunk node must be contained by its own file")
}

// corpusReport accumulates the artifact's rows.
type corpusReport struct {
	crossFileContains int
	sameFileContains  int
	uncontained       int
	collided          int
	stats             resolveStats

	ambiguousKeys map[string]bool
	dynamicKeys   map[string]bool
	byLanguage    map[string]int
	byEdgeType    map[string]int

	// ecma carries the per-ECMAScript-language rows this ticket is judged on.
	ecma map[string]*ecmaRow

	// goRows carries Go's rows; its type and render live in
	// corpus_verification_go_test.go, which this file is too close to the
	// 500-line cap to host.
	goRows *goRow

	// siblingSkips carries the sibling-rung transition — the population the
	// per-language gate suppressed, across EVERY language rather than only the
	// four the two row-verifiers walk. Its type, census and render live in
	// corpus_verification_sibling_test.go for the same 500-line reason.
	siblingSkips *siblingSkipCensus

	// testCalls carries the test-attributed rows, SEPARATE from every
	// production counter above. A value rather than a pointer so its rows
	// render as zeros instead of vanishing. Its type, census and render live
	// in corpus_verification_test_calls_test.go.
	testCalls testCallsCensus
}

// ecmaRow is one ECMAScript language's resolution picture.
//
// THE EXTERNAL SPLIT IS THE POINT. One STATUS with FOUR CAUSES: rolled
// together, a resolver that binds NOTHING reports the same external count as
// one that binds everything. The first three all correspond to a RECORDED
// bind, so a run where all three are zero while noBinding is large is the
// signature of an arm that stopped recording — the failure mode the
// indexability re-ruling made the one that matters.
type ecmaRow struct {
	references int
	bound      int
	// collided counts BOUND resolutions that chose one declaration while rivals
	// under the same key survived — the event the replaced scalar symbol map
	// performed silently. It must be ZERO by construction, because cardinality
	// above one classifies as ambiguous and emits a group instead of binding.
	collided int
	// boundUnqualifiedImport and boundQualifiedImport are the two rungs this
	// ticket switches on. Both zero means the arm recorded nothing the ladder
	// could read, whatever the other rows say.
	boundUnqualifiedImport int
	boundQualifiedImport   int

	// boundQualifiedMember and boundQualifiedPath are the two rungs added by the
	// qualified-path work, and they are censused HERE rather than left to fall
	// into the unattributed arm because an instrument that cannot see a rung
	// cannot report it firing OR not firing.
	//
	// A ZERO IN EITHER IS A MEASUREMENT, NOT A BLIND SPOT, and the distinction
	// is the whole reason these rows exist: before them a member bound through
	// an import's container incremented `bound` and no rule row at all, so the
	// picture was indistinguishable from the rung never firing.
	//
	// boundQualifiedMember counts `Foo.method()` reaching a MEMBER of an
	// imported container. It reads zero on a corpus whose imported qualifiers
	// are VALUES rather than classes — an imported const array's `.map`, an
	// object-namespace `api.GET` — because no declaration exists under that
	// parent for any rung to find.
	//
	// boundQualifiedPath counts a FULLY-QUALIFIED reference resolved through its
	// qualifier's own package scope. It is structurally zero for the ECMAScript
	// family, which has no package-qualified reference form at all; a non-zero
	// value here would mean the rung's language set has changed.
	boundQualifiedMember int
	boundQualifiedPath   int

	// siblingMemberBound counts BOUND resolutions that fired the sibling rung:
	// a bare name inside a container reaching a member of that same container.
	//
	// IT IS THE BEFORE HALF OF THE SIBLING-RUNG TRANSITION. All three
	// ECMAScript languages skip that rung once the gate lands, so this row must
	// read ZERO in a post-gate artifact — a non-zero value there says the gate
	// did not fire, which is the check no per-language fixture can make on real
	// data.
	siblingMemberBound int

	ambiguousGroups int
	dynamicGroups   int
	dynamicUnbound  int
	// THE RESIDUE IS ATTRIBUTED, NOT COUNTED. Every dynamic reference reached
	// the dynamic rung through a QUALIFIER, so each one is either a member
	// access (obj.method) or a computed one (obj[key].method). Splitting them
	// is what tells a sanctioned language property from an unresolved gap: a
	// computed access is beyond static analysis by construction, while a member
	// access on a local value is the honest open set the rung exists for.
	dynamicMemberAccess   int
	dynamicComputedAccess int

	// externalQualifier counts terminations at R2X. ZERO here while outOfRepo
	// is large is a DEFECT to report, not a number to accept: it means the
	// binds are recorded but the ladder is not reading the index's scope set.
	externalQualifier int

	external            int
	outOfRepo           int
	outOfIndex          int
	noNamedDeclarations int
	noBinding           int
}

func newCorpusReport() *corpusReport {
	return &corpusReport{
		ambiguousKeys: map[string]bool{},
		dynamicKeys:   map[string]bool{},
		byLanguage:    map[string]int{},
		byEdgeType:    map[string]int{},
	}
}

// countContainment splits file-to-symbol containment into same-file and
// cross-file, and counts chunk nodes that no file contains.
func (r *corpusReport) countContainment(edges []*knowledgev1.Edge, chunkFile map[string]string) {
	contained := map[string]bool{}
	for _, e := range edges {
		r.byEdgeType[e.Type]++
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains {
			continue
		}
		declFile, isChunkTarget := chunkFile[e.ToId]
		if !isChunkTarget {
			continue
		}
		// A file-sourced containment edge: the FromID is a path, so it is not
		// itself a chunk node.
		if _, fromIsChunk := chunkFile[e.FromId]; fromIsChunk {
			continue
		}
		if e.FromId == declFile {
			r.sameFileContains++
			contained[e.ToId] = true
		} else {
			r.crossFileContains++
		}
	}
	for id := range chunkFile {
		if !contained[id] {
			r.uncontained++
		}
	}
}

// countGroups partitions the residue by KIND. The two are never summed: an
// ambiguous group is CLOSED and a dynamic group is OPEN, so a total over both
// would state a property neither has.
func (r *corpusReport) countGroups(edges []*knowledgev1.Edge) {
	for _, e := range edges {
		switch e.Method {
		case kgtypes.EdgeMethodAmbiguousName:
			r.ambiguousKeys[e.Evidence] = true
		case kgtypes.EdgeMethodDynamic:
			r.dynamicKeys[e.Evidence] = true
		}
	}
}

func (r *corpusReport) countLanguages(results []*treesitter.Result) {
	for _, res := range results {
		r.byLanguage[string(res.Language)]++
	}
}

// countCollidedKeyResolutions counts BOUND resolutions that chose one
// declaration while rivals under the same key survived — the exact event the
// replaced scalar map performed silently, 2,778 times plus 446 more that looked
// unique only because a rival had already been overwritten.
//
// It must be ZERO by construction: cardinality above one classifies as
// ambiguous and emits a group instead of binding. Measuring it anyway is what
// makes that a checked property rather than an asserted one — if classify ever
// narrows silently, this row goes non-zero on real data with no other gate
// noticing.
func countCollidedKeyResolutions(ix *declIndex, edges []*knowledgev1.Edge) int {
	bound := map[string]bool{}
	for _, e := range edges {
		if e.Method == "" && kgtypes.EdgeType(e.Type) != kgtypes.EdgeContains &&
			kgtypes.EdgeType(e.Type) != kgtypes.EdgeImports {
			bound[e.ToId] = true
		}
	}
	collided := 0
	for id := range bound {
		rec, ok := ix.byID[id]
		if !ok {
			continue
		}
		rivals := ix.lookup(declKey{Scope: rec.Scope, Parent: rec.Parent, Name: rec.Name})
		if len(rivals) > 1 {
			collided++
		}
	}
	return collided
}

// ecmaLanguages are the three languages this ticket's arm serves.
var ecmaLanguages = []string{"typescript", "tsx", "javascript"}

// ecmaScriptRows attributes every reference of the three ECMAScript languages.
//
// IT IS AN ADAPTER OVER censusWalk (corpus_census_walk_test.go) AND NO LONGER A
// WALK OF ITS OWN. The measurement is shared with goRows and censusByLanguage;
// what stays here is the SHAPING — ecmaRow's named per-rule counters, which
// renderECMAScript writes and which are the shared boundByRule map read at this
// family's rungs — plus the four-way external cause split below, which keys on
// a FILE-scope test no other family can take.
//
// The census still re-walks rather than reading resolveEdgesWithStats's
// aggregate, for the reason it always did: that aggregate is global and carries
// no rule or per-language split, and the acceptance question is entirely about
// which RULE fired for which LANGUAGE.
func ecmaScriptRows(results []*treesitter.Result, ix *declIndex, files []string) map[string]*ecmaRow {
	discovered := make(map[string]bool, len(files))
	for _, f := range files {
		discovered[f] = true
	}
	rows := map[string]*ecmaRow{}
	for _, lang := range ecmaLanguages {
		rows[lang] = &ecmaRow{}
	}

	census := censusWalk(results, ix, censusSpec{
		languages: ecmaLanguages,
		admits:    admitAllReferences,
		hooks: censusHooks{
			// THE CAUSE SPLIT STAYS IN THIS FILE. It is reached during the walk
			// rather than after it because it reads the reference's own bind
			// table, which no row carries.
			onExternal: func(lang string, ref *treesitter.RefSite, target string) {
				rows[lang].attributeExternal(ref, target, discovered)
			},
		},
	})
	// censusWalk pre-creates a row for every NAMED language, so each of the
	// three is present even on a corpus holding none of that language — which is
	// what keeps renderECMAScript emitting all three blocks as zeros.
	for _, lang := range ecmaLanguages {
		rows[lang].fill(census[lang])
	}
	return rows
}

// fill copies the shared census onto this family's row shape. It deliberately
// touches none of the four external cause counters: those are filled by the
// onExternal hook during the walk, one reference at a time.
func (row *ecmaRow) fill(c *censusRow) {
	row.references = c.references
	row.bound = c.bound
	row.collided = c.collided
	row.boundUnqualifiedImport = c.boundByRule[string(RuleUnqualifiedImport)]
	row.boundQualifiedImport = c.boundByRule[string(RuleQualifiedImport)]
	row.boundQualifiedMember = c.boundByRule[string(RuleQualifiedMember)]
	row.boundQualifiedPath = c.boundByRule[string(RuleQualifiedPath)]
	row.siblingMemberBound = c.boundByRule[string(RuleSiblingMember)]
	row.ambiguousGroups = c.ambiguous
	row.dynamicGroups = c.dynamicGroups
	row.dynamicUnbound = c.dynamicUnbound
	row.dynamicMemberAccess = c.dynamicMemberAccess
	row.dynamicComputedAccess = c.dynamicComputedAccess
	row.externalQualifier = c.externalQualifier
	row.external = c.external
}

// attributeExternal assigns one external reference to a cause.
//
// The first three causes are all RECORDED binds and are told apart by what the
// bind's scope names: nothing at all (out of repo), a path no file was
// discovered at (out of index), or a discovered file that contributed no
// declaration (no named declarations). The fourth is the absence of a bind.
func (row *ecmaRow) attributeExternal(ref *treesitter.RefSite, target string, discovered map[string]bool) {
	key := target
	if i := strings.LastIndexByte(target, '.'); i >= 0 {
		key = target[:i]
	}
	bind, bound := ref.Binds[key]
	if !bound {
		row.noBinding++
		return
	}
	switch {
	case bind.Scope == "":
		row.outOfRepo++
	case discovered[strings.TrimPrefix(bind.Scope, "file:")]:
		row.noNamedDeclarations++
	default:
		row.outOfIndex++
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
