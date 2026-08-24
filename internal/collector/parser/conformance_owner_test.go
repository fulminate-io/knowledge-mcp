// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// TestDeclaredConformanceMemberOwnership pins the rule that a member
// relationship lands on the CONFORMING declaration's member and never on a
// same-named sibling container's.
//
// THE DEFECT THIS REPRODUCES was found by reading real corpus edges, not by
// inspection. A member is indexed under {Scope, Parent, Name} with Parent a
// BASE name, so two containers sharing a scope and a base name index their
// members under one key. Where the conforming container declares no member of
// that name and its SIBLING does, the lookup returned exactly one candidate —
// the sibling's — so the ambiguity rule never fired and a relationship was
// emitted pointing at a declaration that is not the subtype's member.
//
// EACH SHAPE CARRIES A KNOWN-POSITIVE CONTROL IN THE SAME FIXTURE, and the
// controls are what make the absences meaningful: a run that emitted no member
// relationships at all would satisfy every NotContains here forever.
func TestDeclaredConformanceMemberOwnership(t *testing.T) {
	t.Run("scala_companion_object_and_trait", func(t *testing.T) {
		// THE LIVE CORPUS SHAPE, reduced. `object Store` conforms to
		// StoreFunctions and declares only `apply`; the COMPANION TRAIT of the
		// same name declares `put`, which is also the name StoreFunctions
		// declares. Before the ownership check the supertype's `put` paired
		// with the TRAIT's `put`.
		const src = `package app

trait StoreFunctions {
  def put(k: String): Unit = ()
  def wipe(): Unit = ()
}

trait Store {
  def put(k: String): Unit
}

object Store extends StoreFunctions {
  def apply(): Store = null
  def wipe(): Unit = ()
}
`
		edges := ownerFixtureEdges(t, []fixtureFile{{path: "app/store.scala", src: src}})
		require.NotEmpty(t, edges, "control: the fixture produced declared-conformance relationships at all")

		require.False(t,
			ownerEdgeExists(edges, "app/store.scala:StoreFunctions.put", "app/store.scala:Store.put"),
			"the conforming OBJECT declares no `put`; its companion TRAIT does, and a member "+
				"relationship must not cross to the sibling that happens to share the base name")

		// KNOWN-POSITIVE CONTROL, same fixture, same supertype: the object DOES
		// declare `wipe`, so that member still pairs. Without this leg the
		// assertion above would pass against a fix that disabled member pairing
		// for the whole declaration.
		require.True(t,
			ownerEdgeExists(edges, "app/store.scala:StoreFunctions.wipe", "app/store.scala:Store.wipe"),
			"control: a member the CONFORMING declaration really declares must still pair")
	})

	t.Run("csharp_partial_blocks_are_one_type", func(t *testing.T) {
		// C# `partial` BLOCKS ARE ONE TYPE, so a member declared in either block
		// implements an interface the other block names. This case therefore
		// asserts the pair IS emitted — the opposite of every other case in this
		// file — and an earlier revision of it asserted the drop, which was a
		// test pinning a wrong expectation.
		//
		// The discriminator is the language's own `partial` keyword, stated by
		// the C# arm on both blocks; nothing here infers it from shape.
		const src = `interface IWriter {
    void Write();
    void Flush();
}

partial class Server : IWriter {
    public void Flush() {}
}

partial class Server {
    public void Write() {}
}
`
		edges := ownerFixtureEdges(t, []fixtureFile{{path: "app/Server.cs", src: src}})
		require.NotEmpty(t, edges, "control: the fixture produced declared-conformance relationships at all")

		require.True(t,
			ownerEdgeExists(edges, "app/Server.cs:IWriter.Write", "app/Server.cs:Server.Write"),
			"the OTHER partial block's member implements the interface the conforming block names, "+
				"because the two blocks are one type")
		require.True(t,
			ownerEdgeExists(edges, "app/Server.cs:IWriter.Flush", "app/Server.cs:Server.Flush"),
			"control: the conforming block's own member pairs too")
	})

	t.Run("csharp_generic_arities_stay_confined", func(t *testing.T) {
		// THE CROSSING ASSERTION MOVED HERE, off partial blocks and onto a shape
		// the pinned corpus really contains: a family of generic arities
		// sharing one base name, 31 of them in one C# file, none partial.
		//
		// WHAT OWNERSHIP DOES TO THAT FAMILY ON THE CORPUS IS RECOVERY, NOT
		// REMOVAL, and the distinction is worth stating because the opposite
		// was assumed once. Every arity declares its own GetValue, so before
		// ownership the lookup returned all of them, tripped the ambiguity rule
		// and DECLINED — one member pair in that file, zero wrong edges.
		// Narrowing each container's lookup to the members it owns turns the
		// ambiguity into an answer: 32 pairs, every one owner-correct.
		//
		// The fixture still asserts a DECLINE, and both facts hold at once: an
		// arity that declares the member pairs with it, and an arity that does
		// not must not borrow another's. Distinct arities are distinct types,
		// which is what makes this the counterpart to the partial case above
		// rather than a contradiction of it.
		const src = `interface ISnapshot {
    T GetValue<T>(int index);
    void Clear();
}

sealed class Snapshot<T0> : ISnapshot {
    public void Clear() {}
}

sealed class Snapshot<T0, T1> {
    public T GetValue<T>(int index) => default;
}
`
		edges := ownerFixtureEdges(t, []fixtureFile{{path: "app/Snapshot.cs", src: src}})
		require.NotEmpty(t, edges, "control: the fixture produced declared-conformance relationships at all")

		var crossed bool
		for _, e := range edges {
			if e.FromId == "app/Snapshot.cs:ISnapshot.GetValue" {
				crossed = true
			}
		}
		require.False(t, crossed,
			"the conforming arity declares no GetValue; the OTHER arity does, and distinct arities "+
				"are distinct types — crossing between them is a wrong edge")
		require.True(t,
			ownerEdgeExists(edges, "app/Snapshot.cs:ISnapshot.Clear", "app/Snapshot.cs:Snapshot.Clear"),
			"control: the member the conforming arity really declares still pairs")
	})

	t.Run("containers_differing_only_in_parent_stay_confined", func(t *testing.T) {
		// THE SHAPE A SHARED-CONTAINER-KEY TEST CANNOT SEE, and the reason
		// ownership is now the predicate on both sides. A member key names its
		// container by BASE NAME and drops the container's own Parent, so two
		// traits of one name nested in DIFFERENT objects collide on members
		// while their own container keys stay distinct — the shared-key question
		// answers false for both and a narrowed filter no-ops.
		//
		// Reproduced first-hand on this tree before the fix: the pass emitted
		// GenTupleInstances.gen -> Template.gen where that member's owner was
		// the OTHER object's Template, with the ambiguity counter reading zero.
		const src = `package app

trait GenTupleInstances {
  def gen(): Unit
  def keep(): Unit
}

object AlgebraBoilerplate {
  trait Template extends GenTupleInstances {
    def keep(): Unit = ()
  }
}

object KernelBoiler {
  trait Template {
    def gen(): Unit = ()
  }
}
`
		ix := ownerFixtureIndex(t, []fixtureFile{{path: "app/boiler.scala", src: src}})
		pairs, stats := deriveDeclaredConformance(ix)
		require.Len(t, pairs, 1, "control: the type-level relationship is derived")

		require.False(t, ownerAnySpecPaired(pairs, "app/boiler.scala:GenTupleInstances.gen"),
			"the conforming trait declares no `gen`; the same-named trait nested in ANOTHER object "+
				"does, and their container keys differ so no shared-key test could catch it")
		require.True(t,
			ownerMemberPaired(pairs, "app/boiler.scala:GenTupleInstances.keep", "app/boiler.scala:Template.keep"),
			"control: the member the conforming trait really declares still pairs")
		require.Zero(t, stats.AmbiguousMember,
			"and the ambiguity counter reads zero here, which is why this shape was invisible")
	})

	t.Run("php_same_namespace_across_files", func(t *testing.T) {
		// PHP RESOLVES BY DECLARED NAMESPACE, NOT BY DIRECTORY, so two files
		// declaring `namespace App;` put their declarations in ONE scope — and a
		// class name declared in both then collides at {Scope, Parent, Name}
		// with no path-hash to separate them, because the suffix disambiguates
		// within a file and these are two files.
		//
		// THIS IS THE REALISTIC CORPUS SHAPE rather than a contrived one: a tree
		// that vendors a library twice, or ships it in two build flavors,
		// declares the same class in the same namespace twice. The java corpus
		// this ticket measures does exactly that.
		//
		// The BRACED re-opened form was tried first and is NOT reachable today,
		// for a reason that has nothing to do with ownership: a braced namespace
		// becomes a CONTAINER, so its classes take it as their parent while the
		// supertype lookup asks with an empty parent — the spelling resolves to
		// nothing, the type-level pair never forms, and no member pairing can
		// follow. Measured: that fixture reports unresolvable=1, type_pairs=0.
		conforming := fixtureFile{path: "app/server.php", src: `<?php
namespace App;

interface Writer {
    public function write();
    public function flush();
}

class Server implements Writer {
    public function flush() {}
}
`}
		sibling := fixtureFile{path: "vendor/server.php", src: `<?php
namespace App;

class Server {
    public function write() {}
}
`}
		edges := ownerFixtureEdges(t, []fixtureFile{conforming, sibling})
		require.NotEmpty(t, edges, "control: the fixture produced declared-conformance relationships at all")

		require.False(t,
			ownerEdgeExists(edges, "app/server.php:Writer.write", "vendor/server.php:Server.write"),
			"the conforming class declares no write; the same-named class in the OTHER file of the "+
				"same namespace does, and the relationship must not cross to it")

		require.True(t,
			ownerEdgeExists(edges, "app/server.php:Writer.flush", "app/server.php:Server.flush"),
			"control: the member the CONFORMING class really declares must still pair")
	})

	t.Run("shared_supertype_declines_before_members", func(t *testing.T) {
		// WHY THERE IS NO SUPERTYPE-SIDE OWNERSHIP FILTER, pinned rather than
		// argued in a comment alone. A supertype only reaches member pairing by
		// RESOLVING, and resolution requires exactly one declaration under
		// {Scope, Name} — which is the same question the ownership check asks of
		// a top-level container. So a supertype whose key is shared is declined
		// upstream as AMBIGUOUS and never reaches the member stage: a filter on
		// that side would be a branch no input can reach.
		//
		// A first version of this fix carried that filter and a counter for it.
		// The counter read zero on every corpus, and this fixture is what
		// established the reason was UNREACHABILITY rather than rarity — so the
		// branch was removed instead of shipped as a documented no-op.
		const src = `package app

trait Base {
  def go(): Unit
}

object Base {
  def extra(): Unit = ()
}

class Impl extends Base {
  def go(): Unit = ()
  def extra(): Unit = ()
}
`
		ix := ownerFixtureIndex(t, []fixtureFile{{path: "app/base.scala", src: src}})
		pairs, stats := deriveDeclaredConformance(ix)
		require.Positive(t, stats.AmbiguousSupertype,
			"a supertype spelling naming BOTH a trait and its companion object is ambiguous, and "+
				"the type level declines it")
		require.Empty(t, pairs,
			"so no pair reaches member pairing at all, which is what makes a supertype-side "+
				"ownership filter unreachable")
		require.Zero(t, stats.MemberPairs, "and no member relationship is emitted")
	})

	t.Run("python_wrong_edge_across_same_named_classes", func(t *testing.T) {
		// THE MALIGNANT HALF OF THE DEFECT, and the only shape here that
		// distinguishes a correct fix from one that merely declines. The other
		// fixtures show a member CROSSING that also happens to be declinable;
		// this one showed a WRONG EDGE with nothing to decline, because exactly
		// ONE record carried the member key and the emitter therefore saw no
		// ambiguity at all. Its counters read ambiguous_member=0 while it
		// emitted a member relationship to a class that implements nothing.
		//
		// TWO CLASSES NAMED Sink IN ONE MODULE. The FIRST declares `write` and
		// no base; the SECOND declares the conformance and only `other`. The
		// containers are disambiguated (their node IDs take a path hash) but
		// their members keep the bare parent spelling, so `Sink.write` — owned
		// by the first — sat under the same key the second's members do.
		//
		// THE KNOWN-POSITIVE CONTROL IS IN THE SAME FIXTURE AND IS THE POINT OF
		// IT: the supertype also declares `other`, which the CONFORMING class
		// really does declare, so that pair must still be emitted. A fix that
		// declined the whole declaration would fail here, which is exactly what
		// makes this the sharpest of the five.
		//
		// THE CAPTURE IS INJECTED BECAUSE THIS LANGUAGE HAS NO ARM in this tree;
		// the containment the fix reads is real. See ownerFixtureIndexInjected.
		// ONE MODULE, because python resolves FILE-SCOPED here: a supertype
		// declared in a sibling file does not resolve at all, so a two-file
		// fixture would report unresolvable and never reach member pairing.
		files := []fixtureFile{{path: "svc/m.py", src: "" +
			"class Contract(ABC):\n    def write(self):\n        pass\n\n    def other(self):\n        pass\n" +
			"\n\nclass Sink:\n    def write(self):\n        pass\n" +
			"\n\nclass Sink(Contract):\n    def other(self):\n        pass\n"}}
		var conformingID string
		ix := ownerFixtureIndexInjected(t, files, func(chunk treesitter.Chunk, nodeID string) *treesitter.TypeFacts {
			switch {
			case nodeID == "svc/m.py:Contract":
				return &treesitter.TypeFacts{IsInterface: true}
			case chunk.ChunkType == "class_definition" && chunk.StartLine == 14:
				// The SECOND Sink, selected by source position rather than by
				// name: both carry the same base name and only their position
				// tells them apart.
				conformingID = nodeID
				return &treesitter.TypeFacts{Conforms: []treesitter.DeclaredSupertype{
					{Text: "Contract", Kind: treesitter.ConformImplements},
				}}
			}
			return nil
		})
		require.NotEmpty(t, conformingID, "control: the conforming class was found and given a clause")

		pairs, stats := deriveDeclaredConformance(ix)
		require.Len(t, pairs, 1, "control: exactly one type-level relationship is derived")
		require.Equal(t, "svc/m.py:Contract", pairs[0].supertype.NodeID)
		require.Equal(t, conformingID, pairs[0].subtype.NodeID,
			"control: the TYPE level already picks the right suffixed container — it is the member "+
				"level that used to cross")

		require.False(t, ownerAnySpecPaired(pairs, "svc/m.py:Contract.write"),
			"the conforming class declares no `write`; the OTHER class of the same name does, and "+
				"pairing to it is a WRONG EDGE rather than a decline — there is exactly one record "+
				"under that key, so no ambiguity exists to catch it")

		require.True(t, ownerMemberPaired(pairs, "svc/m.py:Contract.other", "svc/m.py:Sink.other"),
			"control: the member the CONFORMING class really declares must STILL pair, which is "+
				"what separates a correct fix from one that declines everything under a shared name")

		require.Zero(t, stats.AmbiguousMember,
			"and it is not reached by way of the ambiguity counter: that counter reads zero here, "+
				"which is precisely why this shape was invisible")
	})

	t.Run("typescript_declaration_merging_stamps_the_right_owner", func(t *testing.T) {
		// TypeScript reaches the same shared-name shape by DECLARATION MERGING:
		// an interface and a class of one name in one file is legal, idiomatic
		// TypeScript, and the two are separate container declarations here.
		//
		// THIS ASSERTS THE MECHANISM, NOT THE EDGE, AND THE REASON IS MEASURED.
		// This tree does not chunk a TypeScript interface's members at all, so a
		// TypeScript supertype — which is an interface in every idiomatic case —
		// contributes NO member specs, and the member-pairing stage returns
		// before it starts. Both halves of an edge-level assertion would
		// therefore be vacuous here: the absence would hold because the nodes do
		// not exist, and the presence could never hold at all. What IS decidable
		// on this tree is whether the fix's INPUT is right for the shape, so
		// that is what is pinned — the two merged partners are separate records
		// sharing one base name, and the member is stamped to the partner that
		// really declares it.
		//
		// The edge-level half of this shape is covered by the python fixture
		// above, where both partners really do contribute members.
		files := []fixtureFile{{path: "web/m.ts", src: "" +
			"interface Sink {\n  write(): void;\n}\n" +
			"\nclass Sink implements Contract {\n  other(): void {}\n}\n"}}
		ix := ownerFixtureIndexInjected(t, files,
			func(treesitter.Chunk, string) *treesitter.TypeFacts { return nil })

		var ifaceID, classID string
		for id, rec := range ix.byID {
			if rec.Parent != "" {
				continue
			}
			switch rec.NodeID {
			default:
				if rec.Name == "Sink" {
					if ifaceID == "" {
						ifaceID = id
					} else {
						classID = id
					}
				}
			}
		}
		require.NotEmpty(t, ifaceID, "control: the merged partners are indexed")
		require.NotEmpty(t, classID,
			"control: declaration merging really does produce TWO container records sharing one "+
				"base name, which is the condition the ownership check keys on")

		member, ok := ix.byID["web/m.ts:Sink.other"]
		require.True(t, ok, "control: the CLASS partner's member is chunked and indexed")
		require.NotEmpty(t, member.Owner,
			"the member carries an owner, so ownership is decidable for this shape")
		require.Contains(t, []string{ifaceID, classID}, member.Owner,
			"and that owner is one of the two merged partners")

		owner := ix.byID[member.Owner]
		require.Equal(t, "class_declaration", chunkKindOf(t, files, owner.NodeID),
			"specifically the CLASS partner, which is the one that declares it — the interface "+
				"partner must not be credited with a member it does not declare")
	})

	t.Run("unshared_container_is_untouched", func(t *testing.T) {
		// THE NO-OP HALF OF THE RULE, and it is the leg that proves the check is
		// narrow rather than a blanket restriction: where no name is shared, the
		// candidate lists are returned untouched and every member still pairs.
		const src = `interface Greeter {
    void greet();
    void shout();
}

class Server implements Greeter {
    public void greet() {}
    public void shout() {}
}
`
		edges := ownerFixtureEdges(t, []fixtureFile{{path: "app/Plain.java", src: src}})
		require.True(t, ownerEdgeExists(edges, "app/Plain.java:Greeter", "app/Plain.java:Server"),
			"the type-level relationship stands")
		require.True(t, ownerEdgeExists(edges, "app/Plain.java:Greeter.greet", "app/Plain.java:Server.greet"),
			"every member of an unshared container still pairs")
		require.True(t, ownerEdgeExists(edges, "app/Plain.java:Greeter.shout", "app/Plain.java:Server.shout"),
			"both of them, so the rule narrows nothing where nothing is shared")
	})
}

// TestDeclaredConformanceTypeScriptMergedPartners pins the ownership rule for
// TypeScript DECLARATION MERGING, at the emitter rather than through the
// chunker.
//
// WHY THE INDEX IS BUILT BY HAND HERE, WHEN EVERY OTHER CASE CHUNKS REAL
// SOURCE. This tree does not chunk a TypeScript interface's members at all —
// measured: `interface Sink { write(): void }` beside a class of the same name
// yields two CONTAINER chunks and exactly one member chunk, the class's. So the
// record that makes this shape dangerous, a member owned by the INTERFACE
// partner, cannot be produced from source here, and a fixture that chunked the
// source would assert an absence that holds because the node does not exist.
// The records below are therefore built directly, in the shape a tree that DOES
// chunk interface members produces, so the emitter's rule is pinned now rather
// than after the chunker catches up.
//
// THE SHAPE IS THE PYTHON WRONG-EDGE SHAPE THROUGH A DIFFERENT DOOR: exactly
// one record carries the member key, it belongs to the partner that declares no
// conformance, and the conforming partner declares no member of that name — so
// there is no ambiguity for a decline path to catch.
func TestDeclaredConformanceTypeScriptMergedPartners(t *testing.T) {
	const (
		path     = "web/m.ts"
		contract = path + ":Contract"
		iface    = path + ":Sink#9fcf61b0"
		class    = path + ":Sink#379e4159"
	)
	ix := conformIndex(conformFile{path: path, lang: treesitter.LangTypeScript, decls: []gateDecl{
		{nodeID: contract, name: "Contract", facts: conformFacts(true)},
		{nodeID: contract + ".write", name: "write", parent: "Contract"},
		{nodeID: contract + ".other", name: "other", parent: "Contract"},
		// The MERGED INTERFACE partner and the member it declares.
		{nodeID: iface, name: "Sink"},
		{nodeID: path + ":Sink.write", name: "write", parent: "Sink"},
		// The MERGED CLASS partner, which carries the conformance and declares
		// a DIFFERENT member.
		{nodeID: class, name: "Sink", facts: conformFacts(false, declared("Contract", treesitter.ConformImplements))},
		{nodeID: path + ":Sink.other", name: "other", parent: "Sink"},
	}})
	// The owners the resolved containment edges would carry. Set directly
	// because the containment this stands in for cannot be produced here; the
	// stamping itself is exercised by every other case in this file.
	ix.byID[path+":Sink.write"].Owner = iface
	ix.byID[path+":Sink.other"].Owner = class
	ix.byID[contract+".write"].Owner = contract
	ix.byID[contract+".other"].Owner = contract

	pairs, stats := deriveDeclaredConformance(ix)
	require.Len(t, pairs, 1, "control: the type-level relationship is derived")
	require.Equal(t, class, pairs[0].subtype.NodeID,
		"control: the merged CLASS is the subtype, not its interface partner")

	require.False(t, ownerAnySpecPaired(pairs, contract+".write"),
		"the conforming CLASS declares no `write`; its merged INTERFACE partner does, and pairing "+
			"to it is a wrong edge with no ambiguity to catch it")
	require.True(t, ownerMemberPaired(pairs, contract+".other", path+":Sink.other"),
		"control: the member the conforming class really declares must still pair")
	require.Zero(t, stats.AmbiguousMember,
		"and the ambiguity counter reads zero here, which is why this shape was invisible")
}
