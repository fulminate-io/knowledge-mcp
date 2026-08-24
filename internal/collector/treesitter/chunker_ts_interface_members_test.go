// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkNamesByParent groups a result's chunk names by the parent each carries.
func chunkNamesByParent(res *Result) map[string][]string {
	out := map[string][]string{}
	for _, ch := range res.Chunks {
		if ch.ChunkType == "comment" {
			continue
		}
		out[ch.ParentName] = append(out[ch.ParentName], ch.Name)
	}
	return out
}

// chunkFor returns the chunk with one parent and name, or nil.
func chunkFor(res *Result, parent, name string) *Chunk {
	for i := range res.Chunks {
		if ch := &res.Chunks[i]; ch.ParentName == parent && ch.Name == name {
			return ch
		}
	}
	return nil
}

// TestTSInterfaceMemberChunks covers the two declaration surfaces this ticket
// adds to the TypeScript grammar: an interface's members become declarations in
// their own right, and an abstract class becomes a NAMED declaration whose
// members are parented to it.
//
// THE SECOND ONE IS A DEFECT FIX, not an addition. Before it, an abstract class
// chunked as an UNNAMED abstract_class_declaration — dropped by the declaration
// index's empty-name guard, so the class was absent from the index entirely —
// and every one of its members carried an empty parent, colliding with every
// other unparented member of the same name in the file.
func TestTSInterfaceMemberChunks(t *testing.T) {
	const ifaceSrc = `export interface Sink {
  write(c: Config): void;
  run: (c: Config) => void;
}

export class FileSink {
  write(c: Config): void {}
}
`

	t.Run("method_signature_is_a_declaration", func(t *testing.T) {
		res := chunkQualFixture(t, "web/contract.ts", ifaceSrc)
		byParent := chunkNamesByParent(res)

		// BOTH MEMBER KINDS, because a TypeScript contract legitimately declares a
		// callable as a property signature as well as a method signature, and a
		// member set admitting only one of the two is a contract half-described.
		assert.Contains(t, byParent["Sink"], "write", "a method_signature is a declaration")
		assert.Contains(t, byParent["Sink"], "run", "a property_signature is a declaration")

		write := chunkFor(res, "Sink", "write")
		require.NotNil(t, write, "control: the interface member chunk exists at all")
		assert.Equal(t, "method_signature", write.ChunkType)

		// THE TWO MEMBER KINDS STAY DISTINGUISHABLE. A contract may declare a
		// callable either way, and a reader of the graph should be able to tell
		// which the source wrote rather than seeing both flattened to one kind.
		run := chunkFor(res, "Sink", "run")
		require.NotNil(t, run, "control: the property-signature member chunk exists at all")
		assert.Equal(t, "property_signature", run.ChunkType)
	})

	t.Run("parent_is_the_interface", func(t *testing.T) {
		// THE THREE COMPONENTS THE NODE ID IS COMPOSED FROM ARE ASSERTED HERE,
		// rather than the composed string: the composer lives in the parser
		// package, which imports this one, so calling it from here would invert
		// the dependency. The COMPOSED id — web/contract.ts:Sink.write — is
		// asserted at the parser level by the two-hop contract test, which is also
		// the place the id has to be right for something to depend on it.
		res := chunkQualFixture(t, "web/contract.ts", ifaceSrc)
		write := chunkFor(res, "Sink", "write")
		require.NotNil(t, write, "control: the interface member chunk exists at all")
		assert.Equal(t, "web/contract.ts", write.FilePath)
		assert.Equal(t, "Sink", write.ParentName)
		assert.Equal(t, "write", write.Name)

		// The class member of the same name keeps its own parent, so the two do
		// not collide — which is the whole reason the interface member needs one.
		impl := chunkFor(res, "FileSink", "write")
		require.NotNil(t, impl, "control: the implementing class member chunk exists")
		assert.Equal(t, "FileSink", impl.ParentName)
	})

	t.Run("abstract_class_member_is_parented", func(t *testing.T) {
		const src = `abstract class Abs {
  write(c: Config): void {}
}

export abstract class EAbs {
  m(c: Config): void {}
}
`
		res := chunkQualFixture(t, "web/abs.ts", src)
		byParent := chunkNamesByParent(res)

		// THE CLASS IS NAMED. An unnamed declaration is dropped by the index's
		// empty-name guard, so "the chunk exists" is not enough — it has to carry
		// the name the members will be filed under.
		assert.Contains(t, byParent[""], "Abs", "a plain abstract class is a named top-level declaration")
		assert.Contains(t, byParent[""], "EAbs", "an exported abstract class is a named top-level declaration")

		// AND THE MEMBERS REACH IT. Before this fix both carried an empty parent.
		assert.Contains(t, byParent["Abs"], "write", "an abstract class's member takes the class as its parent")
		assert.Contains(t, byParent["EAbs"], "m", "an EXPORTED abstract class's member does too")

		assert.NotContains(t, byParent[""], "write", "the member must not remain unparented")
		assert.NotContains(t, byParent[""], "m", "the exported form must not remain unparented either")
	})

	t.Run("tsx_grammar_parity", func(t *testing.T) {
		// tsx rides the same query set through a DIFFERENT grammar, so every arm
		// added above has to be matched by that grammar too.
		res := chunkQualFixture(t, "web/contract.tsx", ifaceSrc)
		byParent := chunkNamesByParent(res)
		assert.Contains(t, byParent["Sink"], "write", "tsx chunks interface members the same way")
		assert.Contains(t, byParent["Sink"], "run", "tsx chunks property signatures the same way")

		write := chunkFor(res, "Sink", "write")
		require.NotNil(t, write, "control: the tsx interface member chunk exists at all")
		assert.Equal(t, "method_signature", write.ChunkType)
	})
}
