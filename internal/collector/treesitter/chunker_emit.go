// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// declSlot returns the 1-based Result.Chunks index recorded for a declaration's
// byte range, or 0 when the range has no slot.
//
// Keying by byte range rather than by *sitter.Node pointer is deliberate. The
// pointer-identity caveat documented at chunker_identity.go:370-379 — node
// interning is a property of the vendored pin, and a re-vendor returning a
// fresh wrapper per call would make every lookup miss SILENTLY — applies to
// any new pointer-keyed map just as much as to the existing one, and this
// carrier must not add a second site with that failure mode. A byte range is
// the node's own content, not the library's bookkeeping.
type slotIndex map[byteRange]int

func (s slotIndex) declSlot(node *sitter.Node) int {
	return s[byteRange{start: node.StartByte(), end: node.EndByte()}]
}

// containerSlot walks a member's ancestor chain and returns the slot of the
// nearest ancestor that is itself a chunked declaration, or 0 when none is.
// The ascent mirrors findEnclosingScope's shape — the walk that produced the
// member's parentName in the first place — so it finds the same container that
// walk named. It replaced a name-based ascent that returned the container's
// disambiguated NAME; a slot needs no name, which is why that one is gone.
//
// ownSlot is EXCLUDED, and that exclusion is load-bearing rather than
// defensive. A byte range does not identify a node uniquely across nesting
// levels: a wrapper holding exactly one child spans exactly its child's bytes,
// so a Python class body containing a single method gives that method's `block`
// the method's own range. Without this guard the ascent's first step matches
// the member itself and emits a member-contains-itself edge — a wrong edge of
// precisely the kind positional addressing exists to prevent.
func (s slotIndex) containerSlot(node *sitter.Node, ownSlot int) int {
	for n := node.Parent(); n != nil; n = n.Parent() {
		if slot := s.declSlot(n); slot != 0 && slot != ownSlot {
			return slot
		}
	}
	return 0
}

// emitDeclarationEdges adds CONTAINS, CALLS (or TEST_CALLS), USES_TYPE, and
// EMBEDS edges to result.Edges.
//
// Every edge carries its addressing with it, so nothing has to be recovered by
// string-parsing later. Containment edges keep the name-built endpoints they
// have always carried AND additionally carry a chunk slot; the parser's
// pre-pass overwrites an endpoint from its slot wherever one exists. The slot
// is authoritative and the name is a legacy carrier — keeping both is what
// makes this layer's output a strict superset of its previous output, so
// assertions that pin containment endpoint names stay meaningful.
//
// The three reference shapes keep their captured ToID and gain the reference
// site, the emitting declaration's byte offset, and their own FromChunk slot.
//
// testOrigin marks a declaration the caller found lexically INSIDE a test
// block, and it changes exactly one thing: the type its CALL edges carry. Type
// references and embeds are unaffected, because the distinction being drawn is
// about call structure and nothing else.
func (c *Chunker) emitDeclarationEdges(
	declNode *sitter.Node,
	src []byte,
	filePath string,
	lang Language,
	fileCtx ChunkContext,
	chunkType, name, parentName string,
	typeRefAlias map[string]string,
	cqs *compiledQuerySet,
	slots slotIndex,
	ref *RefSite,
	testOrigin bool,
	result *Result,
) {
	// Compute the parent-qualified symbol name for edge IDs — "Receiver.Method"
	// for a Go method, "Class.member" elsewhere — to avoid collisions when
	// several parents in the same namespace share a member name. parentName is
	// the same value the chunk carries, so this string equals the declaration
	// key parser/populate builds from the chunk. Pass 2 in walkTopLevel appends
	// "#"+astPathHash to a colliding declaration's OWN name, while a member's
	// parentName was captured in pass 1 and stays unsuffixed. When the collision
	// is on a container (two C++ blocks reopening one namespace, a Rust struct
	// beside its impls), the parent-to-member edge below no longer needs the
	// container's disambiguated name at all: FromChunk addresses the enclosing
	// container POSITIONALLY, and the parser's pre-pass overwrites the name-built
	// FromID from that slot. A type reference naming the container is still
	// repointed through typeRefAlias to whichever colliding declaration is the
	// type, or left alone when more than one candidate survives.
	//
	// The name guard is load-bearing: many TopLevel patterns bind no @name, so
	// their chunks carry Name "" and qualifiedName already returns "" for them,
	// leaving the reference edges inert. Without the guard those endpoints would
	// become a non-empty "<ns>.<parent>." that enters resolution instead of
	// being dropped. Containment is exempt from the guard by construction: it
	// is addressed by slot, and an unnamed declaration has a slot like any other.
	symbolName := name
	if name != "" && parentName != "" {
		symbolName = parentName + "." + name
	}

	ownSlot := slots.declSlot(declNode)
	declRef := refForParent(ref, parentName)

	// File → declaration CONTAINS. FromID is the file path, which is already a
	// graph node ID, and ToID is the qualified name as before; ToChunk makes
	// the target exact so the pre-pass can overwrite that name. This is the
	// shape that closes the unnamed-declaration case: qualifiedName returns ""
	// for a nameless chunk, so the ToID alone left the edge unresolvable and
	// the chunk contained by nothing.
	result.Edges = append(result.Edges, Edge{
		FromID:  filePath,
		ToID:    qualifiedName(fileCtx.PackageName, symbolName),
		Type:    EdgeContains,
		ToChunk: ownSlot,
	})

	// Parent → member CONTAINS: a Go receiver type → its method, and a class →
	// its member in every other language.
	//
	// The container slot comes from the ancestor ascent, which succeeds for
	// every language whose container lexically encloses its member. It does NOT
	// succeed for a Go method: declParentName (chunker_identity.go:214-220)
	// takes a method_declaration's parent from extractGoReceiver, and a
	// receiver TYPE is a SIBLING declaration that may not even live in this
	// file — so no slot can address it and FromChunk stays 0. That edge carries
	// its name and its Ref instead, and the parser resolves the receiver
	// against the declaration index at the reference's own scope, which is
	// Go's own rule that a receiver type is declared in the same package.
	if name != "" && parentName != "" {
		result.Edges = append(result.Edges, Edge{
			FromID:    qualifiedName(fileCtx.PackageName, parentName),
			ToID:      qualifiedName(fileCtx.PackageName, symbolName),
			Type:      EdgeContains,
			FromChunk: slots.containerSlot(declNode, ownSlot),
			ToChunk:   ownSlot,
			Ref:       declRef,
		})
	}

	if name != "" {
		refEdges := c.extractCallEdges(declNode, src, fileCtx.PackageName, symbolName, cqs)
		if testOrigin {
			for i := range refEdges {
				refEdges[i].Type = EdgeTestCalls
			}
		}
		refEdges = append(refEdges,
			aliasTypeRefTargets(c.extractTypeRefEdges(declNode, src, fileCtx.PackageName, symbolName, cqs), typeRefAlias)...)
		result.Edges = append(result.Edges, attachRefSite(refEdges, declRef, declNode, ownSlot)...)
	}

	// For Go struct types: extract EMBEDS edges.
	if lang == LangGo && chunkType == "type_declaration" {
		embeds := extractGoEmbeds(declNode, src)
		embedEdges := make([]Edge, 0, len(embeds))
		for _, embedded := range embeds {
			embedEdges = append(embedEdges, Edge{
				FromID: qualifiedName(fileCtx.PackageName, symbolName),
				ToID:   embedded,
				Type:   EdgeEmbeds,
			})
		}
		result.Edges = append(result.Edges, attachRefSite(embedEdges, declRef, declNode, ownSlot)...)
	}
}

// refForParent returns the reference site a declaration's edges carry.
//
// File, Scope, Lang and Binds are per-FILE and the file-level site is shared
// by every declaration that has no container — the common case, and the reason
// a file costs one site and one Binds map rather than one per edge. Parent,
// though, is the emitting declaration's own container: the sibling-member rule
// resolves an unqualified reference against {Scope, Parent, name}, so a member
// of one class must not see another class's parent. A parented declaration
// therefore takes a derived site, which copies four scalar fields and SHARES
// the Binds map header rather than duplicating it.
func refForParent(fileRef *RefSite, parentName string) *RefSite {
	if parentName == "" {
		return fileRef
	}
	derived := *fileRef
	derived.Parent = parentName
	return &derived
}

// attachRefSite stamps the reference carrier onto every edge in a batch: the
// per-file *RefSite pointer (the SAME pointer for every reference edge in the
// file — one pointer word per edge, never a copy), the emitting declaration's
// StartByte, and that declaration's own chunk slot.
//
// RefByte must be read here, at emission, while the node is in hand: it cannot
// be recovered downstream, because parser/populate.go sorts each result's
// chunks by StartByte before resolution runs and the emission-order slot
// arrangement is gone by then. FromChunk makes a reference edge's own endpoint
// exact by construction, so resolution never has to re-parse the qualified
// string to find out which declaration emitted it.
func attachRefSite(edges []Edge, ref *RefSite, declNode *sitter.Node, ownSlot int) []Edge {
	refByte := int(declNode.StartByte())
	for i := range edges {
		edges[i].Ref = ref
		edges[i].RefByte = refByte
		edges[i].FromChunk = ownSlot
	}
	return edges
}
