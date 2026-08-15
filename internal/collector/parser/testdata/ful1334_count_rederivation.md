# ful1334 — pinned-count re-derivation

Every expectation below was re-derived by RUNNING the test, reading the actual
value, and attributing the movement to a named cause. None was adjusted until a
test went green.

## Census surface

The census is the command, never a number quoted from it:

```
cd <repo root> && grep -rnE '(assert|require)\.(Len|Equal|Empty)\(t,' --include='*_test.go' \
  cmd/knowledge/internal/collector/parser \
  cmd/knowledge/internal/collector/treesitter \
  cmd/knowledge/internal/collector/codesync | grep -vE '/\.claude/worktrees/'
```

Output at the close of this changeset: **263 assertion sites**. The number rose
across the plan's own execution (195 on main at plan time, 207 partway through
Phase 3) because this changeset's new test files are assertion sites too. It is
not a target.

## Causes

- **(a)** orphan chunks gaining containment — `collectOrphans` previously emitted no edge
- **(b)** unnamed declarations — edge already emitted with an empty ToID and dropped at resolution
- **(c)** unnamed test blocks — same shape as (b)
- **(d)** dedup-renamed chunks gaining a containment edge
- **(scope)** a reference that used to bind through a name-wide search now resolves
  in its own scope unit, and lands External when the declaration is outside it
- **(group)** an abstained/ambiguous reference is no longer DROPPED; it emits one
  edge per candidate at Confidence 1/N under one shared Evidence key

## Changed expectations

| File | Test | Old | New | Cause |
| --- | --- | --- | --- | --- |
| `populate_resolve_test.go` | `TestPopulate_PythonEdgesSurvive` | `hasEdge(CALLS, pkg/main.py:run → pkg/animals.py:make_sound)` true | asserted **false** | scope — Python's unit is the FILE; a bare cross-file call has no binding until a BindsResolver arm supplies one |
| `populate_resolve_members_test.go` | `TestPopulate_PolyglotGoNotDegraded` (both orderings) | `hasEdge(CALLS, a/svc.go:run → b/util.go:helper)` true | asserted **false** | scope — a bare Go name resolves in the caller's directory; `helper` is in another package, which Go's own rule also forbids unqualified |
| `container_collision_resolve_test.go` | `TestCollidedTypeRefPrefersTheType/rust_fn_and_struct_no_alias` → renamed `.../rust_fn_and_struct_multi_binds` | `assert.Empty(usesTypeTargets)` — 0 | **2 groups × 2 members** = 4 USES_TYPE edges, Confidence 0.5, Method `ambiguous-name` | group |
| `container_collision_resolve_test.go` | `TestCollidedTypeRefPrefersTheType/csharp_reopened_namespace_zero_survivors` → renamed `.../csharp_reopened_namespace_multi_binds` | namespace targets absent; 4 class-targeting edges | namespace refs form **2 groups × 2 members**; **2** class-targeting edges bind | group, and see the open item below for the drop from 4 to 2 |
| `codesync/callgraph_go_test.go` | `TestBuildGoCallGraph` | candidate pair `parser.resolveEdges → parser.resolveEdgeID` | two pairs naming `parser.resolveEdgesWithStats` and `parser.resolveReference` | the named callee was deleted with the name-map resolver it belonged to |
| `treesitter/chunker_container_test.go` | `TestContainerCollision` (3 collision subtests + the control) | parent-to-member `FromID` equals the container's hash-SUFFIXED name | the edge's `FromChunk` slot resolves to the container chunk that lexically ENCLOSES the member | see the plan-contradiction note below |

## Files in this changeset

Changed or new collector test files, all re-run green:

- `cmd/knowledge/internal/collector/codesync/callgraph_go_test.go`
- `cmd/knowledge/internal/collector/parser/container_collision_resolve_test.go`
- `cmd/knowledge/internal/collector/parser/edges_test.go`
- `cmd/knowledge/internal/collector/parser/indexer_chunk_order_test.go`
- `cmd/knowledge/internal/collector/parser/invariant_containment_test.go`
- `cmd/knowledge/internal/collector/parser/invariant_declindex_test.go`
- `cmd/knowledge/internal/collector/parser/populate_resolve_members_test.go`
- `cmd/knowledge/internal/collector/parser/populate_resolve_test.go`
- `cmd/knowledge/internal/collector/parser/resolve_group_test.go`
- `cmd/knowledge/internal/collector/parser/resolution_matrix_test.go`
- `cmd/knowledge/internal/collector/parser/resolve_walk_test.go`
- `cmd/knowledge/internal/collector/parser/slot_edges_test.go`
- `cmd/knowledge/internal/collector/treesitter/chunker_container_test.go`
- `cmd/knowledge/internal/collector/treesitter/chunker_refsite_test.go`

## Counts that did NOT move, checked rather than assumed

- **Endpoint assertions in the treesitter package.** The chunker still emits its
  name-built containment endpoints and merely adds slots. `go test
  ./internal/collector/treesitter/` is green with no expectation edited.
- **Language-hub edges.** `TestChunkResultsToPopulate_EmitsLanguageEdgePerSymbol`
  passes unchanged; the `allEdges` accumulator still carries exactly those edges.
- **`TestCollidingContainerEdgeResolves`** — 6 subtests, unchanged. It exercises
  parent-to-member resolution, not alias abstention.
- **The four DECIDED subtests of `TestCollidedTypeRefPrefersTheType`**
  (`rust_impl_first_struct_second`, `scala_object_first_class_second`,
  `rust_uncollided_control`, `cpp_reopened_namespace_no_alias`). These are the
  cases where the alias rule picks a winner, and Phase 4's suffix filter exists
  to keep them green. A red here would be a defect, not a re-derivation.

## A CONTRADICTION INSIDE THE PLAN — resolved toward the slot, flagged here

Two plan statements disagree about `TestContainerCollision`, and the disagreement
is only visible once Phase 6 Step 1 runs.

- **Phase 2 Step 4** says: "ENDPOINT ASSERTIONS DO NOT MOVE… every
  treesitter-layer assertion comparing a CONTAINS FromID or ToID to a name still
  passes unchanged. If any goes red, the implementation stopped setting names
  rather than adding slots — a defect, not an expectation to re-derive."
- **Phase 6 Step 1** says: replace `names.parentEdgeName(p)` with `p.parentName`
  at the emission site, and delete `parentEdgeName` with the `suffixed` map.

The second necessarily reds the first: `parentEdgeName` was precisely what put a
container's DISAMBIGUATED name on the parent-to-member `FromID`, so removing it
changes that endpoint from `cpp:pkg.app#3ef44a2d` to `cpp:pkg.app` for a
collided container. The two cannot both be honored.

**Resolved toward Phase 6 Step 1's explicit instruction, after verifying the
invariant still holds by a different mechanism.** Probed on the reopened-C++
fixture: member `a` carries `FromChunk=1` (the block named `app#3ef44a2d`) and
member `b` carries `FromChunk=3` (the block named `app#c28ed2d3`) — each member
addresses the block that lexically encloses it, exactly as before, positionally
instead of by name. The end-to-end gate agrees: `TestCollidingContainerEdgeResolves`
is green at 6 subtests, so a collided container's members still RESOLVE to the
right container after the parser's slot pre-pass.

The test was therefore re-derived onto the slot rather than the name, which is
strictly more exact — a slot identifies one chunk, a name identified one only
while the suffixing scheme held. **The orchestrator should confirm this reading**;
the alternative is keeping `parentEdgeName`, which contradicts Phase 6 Step 1's
census and its "dead code" finding.

## Open item for the orchestrator — a coverage loss inside the C# case

The C# fixture's class-targeting USES_TYPE edges dropped from 4 to 2, and the
mechanism is worth a disposition rather than a silent acceptance.

Two raw references target class `A`: one emitted by the class declaration
itself (RefSite `Parent="App"`, which binds by the sibling-member rule), and one
emitted by the enclosing `namespace App` block (RefSite `Parent=""`, because a
namespace block is itself top-level). The second now lands External: the
class is keyed under `Parent="App"`, and the namespace block's own site does not
carry its OWN name as Parent, so no rung looks under that key.

This is a container's reference to its own member failing to bind, and it will
recur in every language whose members are parented to a container that is also
a scope-bearing block. It sits inside the USES_TYPE reduction this ticket
accepts (34,481 → roughly 19,868), so it is not a new class of loss — but
whether a declaration's RefSite should carry its own name when the declaration
is itself a container is a ladder question the per-language tickets should
answer deliberately. Not changed here: the ladder is prescriptive in this plan
and widening it unilaterally is exactly the freelancing the plan forbids.
