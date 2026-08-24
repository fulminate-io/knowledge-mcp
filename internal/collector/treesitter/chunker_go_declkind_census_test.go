// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNonGoOutputUnchangedByImplements is the non-Go invariance guard: the
// interface work is registered for Go alone, so every other language's output
// must be byte-identical to what it was.
func TestNonGoOutputUnchangedByImplements(t *testing.T) {
	t.Run("no_other_query_names_go_kinds", func(t *testing.T) {
		dir, err := os.Getwd()
		require.NoError(t, err)
		entries, err := filepath.Glob(filepath.Join(dir, "queries_*.go"))
		require.NoError(t, err)
		require.NotEmpty(t, entries, "control: the query files were found at all")

		var goSawKind bool
		checked := 0
		for _, path := range entries {
			body, readErr := os.ReadFile(path) //nolint:gosec // this package's own source
			require.NoError(t, readErr)
			text := string(body)
			if filepath.Base(path) == "queries_go.go" {
				// KNOWN-POSITIVE CONTROL, in the same subtest and the same run:
				// the identical probe finds method_elem HERE, so a zero elsewhere
				// is a real absence rather than a broken read.
				goSawKind = strings.Contains(text, "method_elem")
				continue
			}
			checked++
			assert.NotContains(t, text, "method_elem",
				"%s names a GO node kind; the interface arm is Go-only", filepath.Base(path))
			assert.NotContains(t, text, "type_elem",
				"%s names a GO node kind; the embedded-element rule is Go-only", filepath.Base(path))
		}
		require.True(t, goSawKind, "control: queries_go.go DOES name method_elem")
		require.Positive(t, checked, "control: other query files were actually examined")
	})

	t.Run("arms_match_the_census", func(t *testing.T) {
		// The per-language registries this work touches are keyed by Language, so
		// a language with no row-declared arm reaches none of them. DELEGATED to
		// the registries themselves rather than re-derived: a leaked arm shows up
		// as a registered resolver here.
		//
		// THE SUBJECT LIST COMES FROM THE CAPABILITY CENSUS rather than from a
		// literal "these are not Go", and the skip below is what makes it
		// durable: a language armed by later work drops out of the subject set
		// instead of redding against that work. The languages named here are
		// drawn from the DELIBERATELY unarmed end of the census — each declares
		// no type annotation and no supertype this collector reads, so its row
		// states a property of the language rather than a gap in the work, and
		// the subject set does not evaporate the next time a group is armed.
		checked := 0
		for _, lang := range []Language{LangLua, LangBash, LangElm, LangOCaml} {
			row := testTypedQualifierCensus[lang]
			if row.TypeFactsArm || row.QualifierArm {
				continue
			}
			checked++
			assert.NotContains(t, typeFactsResolvers, lang,
				"%s is unarmed in the census but a type-facts arm is registered for it", lang)
			assert.NotContains(t, qualifierTypeResolvers, lang,
				"%s is unarmed in the census but a qualifier-types arm is registered for it", lang)
			assert.Nil(t, typeFactsFor(lang, nil, "type_declaration", nil),
				"%s must reach no type-facts arm", lang)
			assert.Nil(t, qualifierTypesFor(lang, nil, nil),
				"%s must reach no qualifier-types arm", lang)
		}
		require.Positive(t, checked,
			"every language named here is now armed, so this subtest asserted nothing: it needs an unarmed subject")
		// KNOWN-POSITIVE CONTROL: Go DOES reach an arm, so the nils above are
		// language dispatch rather than two functions that always return nil.
		require.NotNil(t, typeFactsResolvers[LangGo], "control: Go has a registered type-facts arm")
		require.NotNil(t, qualifierTypeResolvers[LangGo], "control: Go has a registered qualifier-types arm")

		// AND THE OUTPUT SIDE, which is what the ticket's invariance requirement
		// is actually about: a non-Go file whose grammar HAS interface-like
		// declarations must still produce no method_elem chunk, because the arm
		// that emits them lives in the Go query set alone.
		c := NewChunker()
		t.Cleanup(c.Close)
		for path, src := range map[string]string{
			"app/iface.ts":  "export interface Sink {\n  write(r: string): void;\n}\n",
			"app/Sink.java": "interface Sink {\n  void write(String r);\n}\n",
		} {
			res, err := c.ChunkFile(context.Background(), path, []byte(src))
			require.NoError(t, err)
			require.NotEmpty(t, res.Chunks, "control: %s produced chunks at all", path)
			for _, ch := range res.Chunks {
				assert.NotEqual(t, "method_elem", ch.ChunkType,
					"%s: method_elem is a GO node kind and must not appear in another grammar's output", path)
			}
		}
	})

	t.Run("corpus_counts_unchanged", func(t *testing.T) {
		// The multi-language corpus assertion is testCorpusMatrix's, in
		// chunker_corpus_test.go — the single source of truth for the (language,
		// framework, test-kind) cells every language must still produce. This
		// subtest NAMES that coverage rather than re-deriving it, so a reviewer
		// can see the guard exists and where it lives.
		require.NotEmpty(t, testCorpusMatrix,
			"the per-language corpus matrix is what pins non-Go chunk counts; it must still exist")
		for _, lang := range []Language{LangTypeScript, LangPython, LangJava} {
			require.Contains(t, testCorpusMatrix, lang,
				"%s must still carry a corpus row, or its counts are unguarded", lang)
		}
	})
}

// TestGoDeclKindConsumerCensus walks both internal trees for files filtering on
// a closed set of Go declaration kinds, and requires each to carry a stated
// disposition toward the new method_elem kind.
func TestGoDeclKindConsumerCensus(t *testing.T) {
	root := repoRootForCensus(t)

	found := map[string]bool{}
	// namesMethodElem records, per subject file, whether it names the new
	// vocabulary at all. It is what turns each row's disposition from a claim
	// into an assertion.
	namesMethodElem := map[string]bool{}
	for _, tree := range []string{
		filepath.Join("cmd", "knowledge", "internal"),
		filepath.Join("cmd", "knowledge-server", "internal"),
	} {
		walkRoot := filepath.Join(root, tree)
		require.DirExists(t, walkRoot, "census control: the consumer tree exists")
		err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// TEST files are excluded from the subject set. A test asserting on a
			// declaration kind is not a consumer whose behavior this census
			// governs — it is an assertion about one.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, readErr := os.ReadFile(path) //nolint:gosec // walks this repo's own source tree
			if readErr != nil {
				return readErr
			}
			text := string(body)
			// THE QUOTING IS THE DETECTOR. A tree-sitter query pattern names the
			// same kinds unquoted inside a raw string, and those are the grammar's
			// vocabulary rather than a Go-code decision.
			if !strings.Contains(text, `"function_declaration"`) &&
				!strings.Contains(text, `"method_declaration"`) &&
				!strings.Contains(text, `"type_declaration"`) {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			slashed := filepath.ToSlash(rel)
			found[slashed] = true
			namesMethodElem[slashed] = strings.Contains(text, "method_elem")
			return nil
		})
		require.NoError(t, err)
	}

	byPath := map[string]declKindConsumerRow{}
	for _, row := range goDeclKindConsumerCensus {
		require.NotContains(t, byPath, row.Path, "duplicate census row for %s", row.Path)
		byPath[row.Path] = row
	}

	t.Run("walk_control_fires", func(t *testing.T) {
		// KNOWN-POSITIVE CONTROL, and it runs FIRST. Every assertion below is
		// about set agreement, and two sets that both went empty agree perfectly.
		// A walk that matched nothing — a moved tree, a broken predicate — must
		// fail loudly here rather than pass as a clean census.
		require.NotEmpty(t, found, "census control: the walk found at least one Go decl-kind consumer")
		require.NotEmpty(t, goDeclKindConsumerCensus, "census control: the disposition table is not empty")
	})

	t.Run("every_subject_has_a_row", func(t *testing.T) {
		for path := range found {
			assert.NotEmpty(t, byPath[path].Path,
				"%s filters on a closed set of Go declaration kinds and carries NO method_elem "+
					"disposition. Add a row stating opts_in, excluded_by_decision or follow_up, with the "+
					"reason. A silent exclusion is indistinguishable from an oversight.", path)
		}
		for path := range byPath {
			assert.True(t, found[path],
				"the census carries a row for %s, which no longer filters on a Go declaration kind. "+
					"Remove the row, or restore the consumer.", path)
		}
	})

	t.Run("dispositions_are_accurate", func(t *testing.T) {
		for path := range found {
			row, ok := byPath[path]
			if !ok {
				continue // reported by every_subject_has_a_row
			}
			assert.NotEmpty(t, strings.TrimSpace(row.Reason),
				"%s carries a disposition with no reason", path)
			// A row that merely EXISTS proves only that someone once wrote a
			// sentence; it does not stay true as the file changes. So each
			// disposition is checked against whether the file actually names the
			// new kind. Producers are exempt: they DECLARE the vocabulary, which
			// says nothing about consuming it.
			switch row.Disposition {
			case dispositionOptsIn:
				assert.True(t, namesMethodElem[path],
					"%s is censused as opts_in but never names method_elem. Either it never opted "+
						"in, or the opt-in was reverted and the row was not.", path)
			case dispositionExcluded, dispositionFollowUp:
				assert.False(t, namesMethodElem[path],
					"%s names method_elem but is censused as %q. A file that reads the new kind has "+
						"opted in; move the row to opts_in with the reason.", path, row.Disposition)
			case dispositionProducer:
			default:
				assert.Failf(t, "unknown disposition",
					"%s carries disposition %q, which is not one of the four", path, row.Disposition)
			}
		}
	})
}
