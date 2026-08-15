// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// cIncludeFixture builds one translation unit whose quoted and angle includes
// are the arm's input.
func cIncludeFixture(path string, lang Language, includes ...string) *Result {
	return &Result{
		FilePath: path,
		Language: lang,
		Chunks:   []Chunk{{Context: ChunkContext{Imports: includes}}},
	}
}

// TestCIncludeSearchLadder pins the four rungs a quoted include is searched
// along, ONE SUBTEST PER RUNG plus the precedence case — because a ladder whose
// members are all present proves membership and says nothing about ORDER, and
// order is what the C standard actually specifies for the quoted form.
func TestCIncludeSearchLadder(t *testing.T) {
	rc := &RepoContext{}

	t.Run("including_dir_wins_when_present", func(t *testing.T) {
		// THE CHARACTERIZATION GUARD: rung 1 is the entire pre-fix behavior and
		// the C standard's own rule for the quoted form, so it must survive the
		// three rungs added below it.
		header := declFile("src/util/helper.h", LangC, "helper")
		self := cIncludeFixture("src/util/main.c", LangC, "helper.h")

		got := BindsFor(rc, map[string]*Result{"src/util/helper.h": header}, self)
		assert.Equal(t, map[string]Bind{"helper": {Scope: "file:src/util/helper.h"}}, got.Binds)
	})

	t.Run("repo_root_relative_include_resolves", func(t *testing.T) {
		// THE LEVELDB SHAPE, and the largest measured population: db/dbformat_test.cc
		// includes "db/dbformat.h", which rung 1 looks up at db/db/dbformat.h.
		header := declFile("db/dbformat.h", LangCPP, "InternalKey")
		self := cIncludeFixture("db/dbformat_test.cc", LangCPP, "db/dbformat.h")

		got := BindsFor(rc, map[string]*Result{"db/dbformat.h": header}, self)
		assert.Equal(t, map[string]Bind{"InternalKey": {Scope: "file:db/dbformat.h"}}, got.Binds,
			"a repository-root-relative include is the shape rung 1 alone cannot reach")
	})

	t.Run("include_dir_rung_resolves", func(t *testing.T) {
		header := declFile("include/leveldb/db.h", LangCPP, "DB")
		self := cIncludeFixture("db/db_impl.cc", LangCPP, "leveldb/db.h")

		got := BindsFor(rc, map[string]*Result{"include/leveldb/db.h": header}, self)
		assert.Equal(t, map[string]Bind{"DB": {Scope: "file:include/leveldb/db.h"}}, got.Binds)
	})

	t.Run("src_dir_rung_resolves", func(t *testing.T) {
		header := declFile("src/lib/thing.h", LangC, "Thing")
		self := cIncludeFixture("tools/run.c", LangC, "lib/thing.h")

		got := BindsFor(rc, map[string]*Result{"src/lib/thing.h": header}, self)
		assert.Equal(t, map[string]Bind{"Thing": {Scope: "file:src/lib/thing.h"}}, got.Binds)
	})

	t.Run("precedence_including_dir_beats_repo_root", func(t *testing.T) {
		// THE ONE CASE THAT PROVES THE ORDER RATHER THAN THE MEMBERSHIP: the
		// same include path is present at BOTH rungs, and only the rung order
		// decides which header's declarations are bound.
		near := declFile("src/util/opt.h", LangC, "near")
		far := declFile("util/opt.h", LangC, "far")
		self := cIncludeFixture("src/util/main.c", LangC, "util/opt.h")

		got := BindsFor(rc, map[string]*Result{
			"src/util/util/opt.h": near,
			"util/opt.h":          far,
		}, self)
		assert.Equal(t, map[string]Bind{"near": {Scope: "file:src/util/util/opt.h"}}, got.Binds,
			"the including file's own directory is the C standard's first rung and must win")
	})

	t.Run("precedence_repo_root_beats_include_dir", func(t *testing.T) {
		// The second ordering pair, for the same reason: rungs 2 and 3 are both
		// satisfiable here and only the order picks one.
		root := declFile("leveldb/db.h", LangCPP, "rootDB")
		inc := declFile("include/leveldb/db.h", LangCPP, "includeDB")
		self := cIncludeFixture("db/db_impl.cc", LangCPP, "leveldb/db.h")

		got := BindsFor(rc, map[string]*Result{
			"leveldb/db.h":         root,
			"include/leveldb/db.h": inc,
		}, self)
		assert.Equal(t, map[string]Bind{"rootDB": {Scope: "file:leveldb/db.h"}}, got.Binds)
	})

	t.Run("angle_include_records_nothing", func(t *testing.T) {
		// An angle include names a system header, it is not in the repo, and C
		// has no NAME to key a bind on — the same shape as java's wildcard
		// import. A rung that resolved <vector> against src/ would bind a
		// repository file to a standard-library spelling.
		self := cIncludeFixture("src/main.c", LangC, "<vector>")

		got := BindsFor(rc, map[string]*Result{"src/vector": declFile("src/vector", LangC, "nope")}, self)
		assert.Empty(t, got.Binds)
	})

	t.Run("unresolvable_include_records_nothing", func(t *testing.T) {
		// NO FABRICATION HERE, and the asymmetry with the rust arm is
		// deliberate: rust binds a NAME the reference writes, so a fabricated
		// scope is what terminates that name; C binds the names it discovers
		// INSIDE a resolved header, so an unresolved include has no names to
		// bind and nothing to terminate.
		self := cIncludeFixture("src/main.c", LangC, "gtest/gtest.h")

		got := BindsFor(rc, map[string]*Result{}, self)
		assert.Empty(t, got.Binds)
	})

	t.Run("a_parented_declaration_is_still_unreachable", func(t *testing.T) {
		// THE KNOWN-NEGATIVE CONTROL for every equality above: the ladder
		// changed which header is found, never which of its declarations are
		// bound. An include names a file rather than a member of a type, so the
		// arm records no Container and a class member stays unbound.
		header := &Result{
			FilePath: "include/a.hpp",
			Language: LangCPP,
			Chunks: []Chunk{
				{Name: "topLevel"},
				{Name: "member", ParentName: "Thing"},
				{Name: ""},
			},
		}
		self := cIncludeFixture("src/main.cpp", LangCPP, "a.hpp")

		got := BindsFor(rc, map[string]*Result{"include/a.hpp": header}, self)
		assert.Equal(t, map[string]Bind{"topLevel": {Scope: "file:include/a.hpp"}}, got.Binds)
	})
}
