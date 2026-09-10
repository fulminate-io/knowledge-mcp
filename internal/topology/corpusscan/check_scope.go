// SPDX-License-Identifier: Apache-2.0

// check_scope.go — the PER-CHECK scope: a check that declares which repository
// and which paths it governs never runs outside them.
//
// WHY A CHECK CARRIES A SCOPE AT ALL. A style rule is a customer's rule, and a
// customer's rule is routinely about one repository or one subtree — a lint
// selector applied to `src/app` and nowhere else. The rule and the check that
// enforces it therefore carry the SAME two scope keys, read here through the
// SAME decoder the practice side reads them with (kgtypes.StyleScopeFromMetadata)
// and matched through the SAME predicate the corpus walk narrows its own scope
// by (parser.MatchesPathPrefixes). Neither is re-implemented here, and that is
// the point of this file rather than an aside: a check side that decided "under
// this path" differently from the rule side would enforce a rule its author
// cannot read off the rule.
//
// A SCOPE THE SCAN CANNOT READ IS A REFUSAL, NEVER A WIDER RUN. An unparseable
// path array, an unmatchable path spelling, an empty array, a repo scope that is
// a path — each is refused BY NAME with the check named, and the check does not
// run. The widest reading of a value the corpus got wrong is exactly the reading
// that runs a customer's `src/app` rule over their whole tree.
//
// AN OUT-OF-SCOPE CHECK IS DISCLOSED, NEVER FOLDED INTO A CLEAN VERDICT. It rides
// as a lead finding on the same terms as a graph check the file scope narrowed
// away: the run answered a narrower question, so it says which check answered
// nothing and why, and a reader never has to tell "this check found nothing"
// from "this check never looked".

package corpusscan

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// checkScopeDecision is what one check's declared scope amounts to for THIS run:
// whether it runs at all, and the file narrowing it runs under.
//
// prefixes IS THE INTERSECTION AND NOT THE CHECK'S OWN PATHS. Two narrowings are
// in force whenever a caller scoped the run — a path_prefix or a file list — and
// a check that replaced the caller's narrowing with its own would scan files the
// caller excluded. Composing them is what makes "scan my diff" and "this rule
// governs pkg/" both true at once.
type checkScopeDecision struct {
	// scope is the check's declaration, exactly as its node carries it.
	scope kgtypes.StyleScope
	// prefixes is the effective ast.Scope.PackagePrefixes for this check: the
	// caller's channel narrowed by the check's paths. Nil means no narrowing,
	// which is the whole repository.
	prefixes []string
	// applies reports that the check runs at all. False means its repo scope
	// names another repository, or its paths and the caller's scope have no
	// intersection.
	applies bool
	// narrowed reports that the CHECK's own scope contributed to prefixes. It is
	// what lets a zero-file walk be attributed to corpus data rather than to the
	// caller's scope, which are different failures with different remedies.
	narrowed bool
}

// resolveCheckScope reads one check's declared scope and composes it with the
// run's own.
//
// THE REPO LEG IS AN EQUALITY AGAINST req.Name, the code-graph instance name —
// the same value ast.Scope.Repo already carries and the value every face
// resolves a repo argument into (an absolute checkout resolves to its basename).
// A scan learns which repository it is scanning from that one field, so the leg
// needs no new input and cannot disagree with the walk about which repo ran.
//
// AN EMPTY SELECTOR NEVER MATCHES A SET SCOPE. Both legs are "absent on the
// check OR matching", never "absent on either side": a run with no repo name
// could not be validated at all (validateScanRequest refuses it), so there is no
// arm where a missing selector silently satisfies a declared scope.
func resolveCheckScope(req foundation.Request, opts scanOptions, entry corpusEntry) (checkScopeDecision, error) {
	sc, err := kgtypes.StyleScopeFromMetadata(entry.Node.GetMetadata())
	if err != nil {
		return checkScopeDecision{}, fmt.Errorf("check %q: %w", entry.Check.ID, err)
	}
	if err := refuseMalformedScope(entry.Check.ID, sc); err != nil {
		return checkScopeDecision{}, err
	}
	dec := checkScopeDecision{scope: sc, prefixes: checkPathPrefixes(req.PathPrefix, opts.files), applies: true}
	// THE REPO LEG DOES NOT SET narrowed, and the omission is deliberate rather
	// than an oversight: narrowed means "the check's paths shaped the FILE set",
	// which is what the zero-walk attribution and the scope-applied line read. A
	// repo mismatch stops the check before any file question arises.
	if sc.RepoSet && sc.Repo != req.Name {
		dec.applies = false
		return dec, nil
	}
	if !sc.PathsSet {
		return dec, nil
	}
	dec.narrowed = true
	dec.prefixes = intersectPrefixes(dec.prefixes, sc.Paths)
	dec.applies = len(dec.prefixes) > 0
	return dec, nil
}

// refuseMalformedScope names every way a declared scope can be unusable.
//
// EACH ARM NAMES THE VALUE AND THE CHECK. A scope is corpus data written by a
// rule author, and the author is the only one who can fix it; "a check has a bad
// scope" is not an answer they can act on.
//
// AN EMPTY PATH ARRAY IS REFUSED RATHER THAN READ AS EITHER DEFAULT. It could be
// read as "everywhere" (the absent key's meaning, which would run a scoped check
// over the whole tree) or as "nowhere" (which would silently disable it). Both
// readings are a guess about a value the author got wrong, so neither is taken.
func refuseMalformedScope(id string, sc kgtypes.StyleScope) error {
	if sc.RepoSet {
		if refusal := kgtypes.StyleScopeRepoRefusal(sc.Repo); refusal != "" {
			return fmt.Errorf("check %q declares %s=%q, which %s", id, kgtypes.MetaKeyStyleScopeRepo, sc.Repo, refusal)
		}
	}
	if !sc.PathsSet {
		return nil
	}
	if len(sc.Paths) == 0 {
		return fmt.Errorf("check %q declares %s as an EMPTY array — an empty scope is neither everywhere nor nowhere, so it is refused rather than read as either; omit the key to run everywhere",
			id, kgtypes.MetaKeyStyleScopePaths)
	}
	for i, p := range sc.Paths {
		if refusal := kgtypes.StyleScopePathRefusal(p); refusal != "" {
			return fmt.Errorf("check %q declares %s element %d as %q, which %s",
				id, kgtypes.MetaKeyStyleScopePaths, i+1, p, refusal)
		}
	}
	return nil
}

// intersectPrefixes composes the run's narrowing with the check's.
//
// THE INTERSECTION OF TWO PREFIX SETS IS THE DEEPER OF EACH OVERLAPPING PAIR,
// and it is computed in both directions because either side can be the deeper
// one: a caller naming `lib/a.go` under a check scoped to `lib` intersects to
// the file, and a caller naming `lib` under a check scoped to `lib/a.go`
// intersects to the same file. A pair that overlaps in neither direction
// contributes nothing, which is how `pkg` and `pkgextra` produce an empty
// intersection rather than a shared parent.
//
// AN EMPTY RESULT IS "NOWHERE" AND NEVER "EVERYWHERE". ast.Scope reads an empty
// PackagePrefixes as no restriction, so the caller of this function must test
// the length and skip the check rather than hand the empty slice to a walk.
//
// THE PREDICATE IS THE WALK'S OWN (parser.MatchesPathPrefixes), so "under this
// path" means path-SEGMENT boundaries here exactly as it does in discovery and
// in the practice-side index. A strings.HasPrefix would admit `pkgextra` under
// `pkg` in this one function and nowhere else in the system.
func intersectPrefixes(run, check []string) []string {
	if len(check) == 0 {
		return run
	}
	if len(run) == 0 {
		return check
	}
	out := make([]string, 0, len(run)+len(check))
	seen := map[string]bool{}
	add := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, r := range run {
		if parser.MatchesPathPrefixes(r, check) {
			add(r)
		}
	}
	for _, c := range check {
		if parser.MatchesPathPrefixes(c, run) {
			add(c)
		}
	}
	return out
}

// scopeColumn renders a decision's declared scope for a disclosure, naming each
// half so a bare value can never be read as the wrong key.
func scopeColumn(sc kgtypes.StyleScope) string {
	var parts []string
	if sc.RepoSet {
		parts = append(parts, kgtypes.MetaKeyStyleScopeRepo+"="+sc.Repo)
	}
	if sc.PathsSet {
		parts = append(parts, kgtypes.MetaKeyStyleScopePaths+"="+strings.Join(sc.Paths, ","))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

// outOfScopeDisclosure records that a check declared a scope this run is outside
// of, so it did not run.
//
// IT IS A DISCLOSURE AND NOT A REFUSAL, and the distinction is the requirement's
// own: a check whose scope excludes this run is working as its author declared,
// while a refusal means the scan could not do what it was asked. So it never
// touches checks_refused and never turns a clean corpus INCONCLUSIVE — and it is
// never absent either, because "this check did not look here" and "this check
// looked and found nothing" are the two sentences a reader must be able to tell
// apart.
//
// reason IS SUPPLIED BY THE CALLER because the two ways to be out of scope are
// established at different moments: the repo and the empty intersection are
// known before the walk, and a scoped walk that opened no file of this language
// is known only after it.
func outOfScopeDisclosure(id string, dec checkScopeDecision, reason string) foundation.Finding {
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityInfo,
		Title:     DisclosurePrefixOutOfScope + id,
		Summary: fmt.Sprintf("check %q declares the scope %s and %s, so it did NOT run for this run. It is recorded rather than "+
			"folded into a clean verdict: a check that answered nothing has not established that anything is clean.",
			id, scopeColumn(dec.scope), reason),
		Evidence: []string{id},
		Metadata: map[string]string{MetaKeyCheckID: id},
	}
}

// scopeAppliedDisclosure records that a check RAN under its own declared
// narrowing, and what that narrowing was.
//
// A NARROWED RUN THAT SAID NOTHING WOULD BE THE SILENT DROP THIS WHOLE SCOPE
// FAMILY CLOSES. A caller who named ten files and read a clean result is entitled
// to know that one check looked at three of them because its author scoped it
// there; without this line the narrowing is invisible in the report and the
// clean verdict reads wider than it is. It is emitted only for a check whose own
// scope was in force, so an unscoped corpus renders exactly as it did before this
// narrowing existed.
func scopeAppliedDisclosure(id string, dec checkScopeDecision) foundation.Finding {
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  foundation.SeverityInfo,
		Title:     DisclosurePrefixScopeApplied + id,
		Summary: fmt.Sprintf("check %q declares the scope %s, so it ran over %s. Its result speaks for those paths "+
			"and for no others — a clean answer from it is not an answer about anything outside them.",
			id, scopeColumn(dec.scope), strings.Join(dec.prefixes, ", ")),
		Evidence: []string{id},
		Metrics:  map[string]float64{MetricScopedPrefixes: float64(len(dec.prefixes))},
		Metadata: map[string]string{MetaKeyCheckID: id},
	}
}

// scopeExclusionReason says WHICH leg excluded a check, in the words a reader
// can act on. The two legs fail for different reasons and have different
// remedies — re-aim the run, or re-scope the check — so one sentence for both
// would tell a reader nothing they did not already know from the title.
func scopeExclusionReason(req foundation.Request, dec checkScopeDecision) string {
	if dec.scope.RepoSet && dec.scope.Repo != req.Name {
		return fmt.Sprintf("this run scans repo %q", req.Name)
	}
	return "none of its paths falls inside this run's own scope"
}

// reasonScopeReachedNoFile is the post-walk leg: the narrowing was legitimate
// and simply holds no file of this corpus's language in this tree.
func reasonScopeReachedNoFile(language string) string {
	return fmt.Sprintf("its paths hold no %s file in this tree", language)
}

// reasonNoGraphCandidate is the graph arm's leg: the check's narrowing left it
// no candidate node to evaluate.
func reasonNoGraphCandidate(nodeType string) string {
	return fmt.Sprintf("no %q node of this code graph is inside its paths", nodeType)
}

// everyCheckOutOfScopeError is the scope-side vacuous-pass closer: the admitted
// set was non-empty and not one member ran, so Run returns an error rather than
// a findings slice.
//
// IT EXISTS BECAUSE A MISTYPED CALLER SCOPE COULD OTHERWISE HIDE BEHIND CORPUS
// SCOPES. The zero-scan guard refuses a caller narrowing that reached no file,
// but a scoped check that never walks reaches that guard through no path at all —
// so over a corpus whose every check is scoped, a path_prefix naming nothing
// would produce a report of disclosures and a CLEAN verdict. This is the same
// refusal everyCheckRefusedError makes for the admission gate, made for the
// scope.
func everyCheckOutOfScopeError(req foundation.Request, set corpusSet, opts scanOptions, refused, outOfScope int) error {
	return fmt.Errorf("topology/%s: not one of the %d check(s) in the %s graph for %s ran against repo %q%s "+
		"(%d out of scope for their declared %s/%s, %d refused)%s — a run that executed nothing is not a clean run",
		AnalyzerName, len(set.Checks), kgtypes.GraphChecks, req.Language, req.Name,
		scopeClause(req.PathPrefix, opts.files), outOfScope,
		kgtypes.MetaKeyStyleScopeRepo, kgtypes.MetaKeyStyleScopePaths, refused, llmOnlySuffix(set))
}
