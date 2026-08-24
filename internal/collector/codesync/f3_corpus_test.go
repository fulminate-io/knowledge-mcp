// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

func TestF3CorpusMeasurement(t *testing.T) {
	root := os.Getenv("F3_CORPUS_ROOT")
	if root == "" {
		t.Skip("set F3_CORPUS_ROOT=<tsparity root> to run the F3 corpus measurement")
	}
	root = filepath.Clean(root)

	t.Run("fixture_control", func(t *testing.T) {
		// THE DETERMINISTIC KNOWN-POSITIVE. It stays even though the agent corpus
		// now carries its own conformance floor, because a corpus is a moving
		// artifact and a fixture is not: if this goes red, every number the two
		// corpus subtests print is suspect, and a clean zero from them would
		// otherwise be indistinguishable from a silent mechanism.
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "web/contract.ts"),
			"export interface Sink {\n  write(): void;\n}\n")
		writeFile(t, filepath.Join(dir, "web/impl.ts"),
			"import { Sink } from './contract';\n\n"+
				"export class FileSink implements Sink {\n  write(): void {}\n}\n\n"+
				"export function send(s: Sink): void {\n  s.write();\n}\n")
		// NOT UNDER bin/. Discovery PRUNES that directory name along with build,
		// dist, out and the rest, so a fixture file placed there is silently
		// absent from the walk — which is exactly the kind of quiet omission the
		// per-kind assertion below exists to catch.
		writeFile(t, filepath.Join(dir, "svc/svc.py"),
			"class Sink(ABC):\n    def write(self):\n        ...\n\n\n"+
				"class FileSink(Sink):\n    def write(self):\n        pass\n\n\n"+
				"def send(s: Sink):\n    s.write()\n")
		writeFile(t, filepath.Join(dir, "app/svc.rb"),
			"module Runner\n  def run\n  end\nend\n\n"+
				"class Worker\n  include Runner\n  def run\n  end\nend\n")
		writeFile(t, filepath.Join(dir, "lib/svc.ex"),
			"defmodule Runner do\n  @callback run(x) :: :ok\nend\n\n"+
				"defmodule Worker do\n  @behaviour Runner\n\n  def run(x) do\n    :ok\n  end\nend\n")

		_, m := measureF3(t, dir, "fixture")
		m.report(t, "fixture")

		assert.Positive(t, f3Sum(m.typedByLang), "the fixture must produce typed-qualifier binds")
		assert.Positive(t, f3Sum(m.conformByKind), "the fixture must produce declared-conformance edges")

		// EVERY KIND THIS TICKET REACHES, ASSERTED BY NAME. A total alone is
		// satisfied by one language carrying the whole count while the other three
		// contribute nothing — which is precisely what happened when the python
		// fixture sat under a pruned directory and the total still looked healthy.
		// Each kind here comes from a different language, so this is also the
		// per-language liveness check.
		for _, kind := range []treesitter.ConformanceKind{
			treesitter.ConformImplements, // typescript
			treesitter.ConformExtends,    // python
			treesitter.ConformMixin,      // ruby
			treesitter.ConformBehaviour,  // elixir
		} {
			assert.Positivef(t, m.conformByKind[string(kind)],
				"the fixture declares a %s relationship, so the harness must see one", kind)
		}
	})

	// SERIALLY, no t.Parallel: each subtest runs Populate over a multi-gigabyte
	// tree twice, and the cost is dominated by memory.
	for _, label := range []string{"agent", "knowledge"} {
		t.Run(label, func(t *testing.T) {
			measureF3Corpus(t, filepath.Join(root, "corpora", label), label)
		})
	}
}

func measureF3Corpus(t *testing.T, dir, label string) {
	t.Helper()
	require.DirExists(t, dir, "control: the frozen corpus is where the manifest says")

	// THE ARM-OFF BASELINE FIRST. A baseline that left the arms registered would
	// compare a number against itself, which is the hazard the registry's
	// Unregister doc comment names.
	restore := disarmF3(t)
	pre, err := parser.Populate(t.Context(), label, dir)
	require.NoError(t, err)
	restore()
	preGroups := callGroups(pre)
	require.NotEmpty(t, preGroups,
		"control: the arm-off baseline produced call groups, so the comparison below is not against an empty set")

	pop, m := measureF3(t, dir, label)
	m.report(t, label)

	// 1. INSTRUMENT LIVENESS. Without it a harness that parsed nothing would
	// report a clean zero-wrong-targets result and look like a pass.
	require.Positive(t, m.callsTotal, "control: the corpus produced CALLS edges at all")

	// 2. WRONG TARGETS ARE ZERO — OVER CALLS EDGES ONLY. Bind-only means the rung
	// may NARROW a group or leave it alone; a post-set that is not a subset of
	// its pre-set has acquired a target the unarmed tree did not offer.
	//
	// THE SCOPE IS NARROWER THAN THE HEADLINE READS, and it is stated here
	// because the filter that imposes it lives a file away in callGroups: this
	// compares CALLS edges and nothing else. IMPLEMENTS edges — including the
	// member-level conformance pairings this ticket adds — are compared against
	// no baseline at all, so a green here says nothing about them. The known
	// exposure on that side is member pairing keying on the parent's BASE name,
	// which can attach a member edge to a same-named sibling container; the
	// collector guide's recorded limitations describe it.
	postGroups, postMethods := callGroupsWithMethods(pop)
	var wrong []string
	for key, post := range postGroups {
		pre, ok := preGroups[key]
		if !ok {
			// A group the arm-off tree did not have at all. The TypeScript query
			// changes add declaration nodes, so a caller that is itself new can
			// legitimately bring new groups with it; only a group whose CALLER
			// existed before can be judged by the subset rule.
			continue
		}
		for target := range post {
			if !pre[target] {
				wrong = append(wrong, key[0]+" -> "+target)
			}
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 && len(wrong) > 20 {
		wrong = wrong[:20]
	}
	assert.Emptyf(t, wrong,
		"[%s] the rung acquired targets the unarmed tree did not offer, which bind-only forbids", label)

	// 2b. EVERY NARROWING IS ATTRIBUTED TO THE RUNG THAT CLAIMS IT.
	//
	// THE SUBSET CHECK ABOVE IS BLIND TO A MIS-NARROWING, and that blindness is
	// worth naming rather than leaving implicit: when the armed tree picks the
	// WRONG member of a group the unarmed tree already offered, the post-set is
	// still a subset of the pre-set and the check passes. Every wrong-target
	// defect found in these arms so far had exactly that shape.
	//
	// WHAT THIS SECOND PROJECTION ADDS, stated narrowly so it is not read as more
	// than it is: where the armed tree collapsed a multi-candidate group to ONE
	// edge, that edge must carry the typed-qualifier rung on Method — so a
	// collapse produced by anything other than a declared type is caught. It does
	// NOT catch the rung choosing wrongly among candidates while correctly
	// claiming the narrowing; nothing without an independent type oracle can, and
	// the per-language negative subtests are what cover that.
	var unattributed []string
	for key, post := range postGroups {
		pre, ok := preGroups[key]
		if !ok || len(pre) <= 1 || len(post) != 1 {
			continue
		}
		for target := range post {
			if postMethods[[2]string{key[0], target}] != string(parser.RuleTypedQualifier) {
				unattributed = append(unattributed,
					key[0]+" -> "+target+" ("+postMethods[[2]string{key[0], target}]+")")
			}
		}
	}
	sort.Strings(unattributed)
	if len(unattributed) > 20 {
		unattributed = unattributed[:20]
	}
	assert.Emptyf(t, unattributed,
		"[%s] a candidate group collapsed to one edge that does NOT claim the typed-qualifier rung, "+
			"so something other than a declared type narrowed it", label)

	// 3. THE AGENT CORPUS CARRIES A DECLARED-CONFORMANCE FLOOR, and its
	// provenance is named so a future reader can re-derive it rather than trust
	// it: that tree contains nine `interface X extends Y` clauses, three of whose
	// supertypes are declared in the SAME FILE — MainTabsContext.tsx (extending
	// MainTabsSnapshot), TrayLayoutContext.tsx (TrayLayoutSnapshot) and
	// EditorialSection.tsx (SectionIntroProps). An interface is a contract, so
	// each resolves and emits with no cross-file dependency at all.
	//
	// THE FLOOR IS >0 AND NOT A COUNT. The number is a property of a tree that
	// sibling tickets and any corpus refresh will move; pinning it would turn a
	// measurement into a false invariant.
	if label == "agent" {
		assert.Positive(t, f3Sum(m.conformByKind),
			"[agent] the corpus's same-file interface-extends clauses must emit declared-conformance edges")
	}
}

func f3Sum(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}
