# HNSW test fixtures

## `hnsw_v2_segment.seg`

A real serialVersion-2 segment blob: 645 bytes, first byte `0x02`, eight nodes
with deterministic synthetic vectors.

**Captured from the pre-conversion writer. NEVER REGENERATE IT.**

This tree can no longer produce a v2 blob. The v2 writer — `serialVersionWithVectors`
and `func (h *binaryGraph) encode()` — was deleted when the offset-addressed
serialVersion-3 layout replaced it, so there is no code path left that emits this
format. The fixture was produced by running the real `Format.Build` + `Encode` of
the pre-conversion source and keeping the bytes.

That is the entire point of checking it in. The rejection test asserts that a
segment written by the OLD format is refused with a remedy telling the operator
to rebuild. A test that generated its own input from the CURRENT tree would be
writing a v3 blob and asserting v3 is rejected — which is not the migration
property, and would pass just as happily if the version check were deleted.

If this file is ever lost, it cannot be recreated from this repository. Recover it
from history: extract the tree at the commit before the v3 conversion landed
(`git archive <sha> | tar -x -C <dir>`), build a segment through that tree's
`Format.Build`, and keep `Encode()`'s bytes.

## `hnsw_v3_ubinary_segment.seg`

A real serialVersion-3 segment blob: 760 bytes, first byte `0x03`, **byte 1 (the
dtype tag) `0x00`**, eight nodes with the same deterministic synthetic vectors.

**Captured from the encoder BEFORE the dtype tag existed. NEVER REGENERATE IT.**

Same discipline as the v2 fixture, for a claim about a different byte. `v3HdrDtype`
occupies what was `v3HdrReserved1`: a byte `encodeGraphV3` never wrote, so every v3
blob any release produced carries `0` there, and `0` is `dtypeUbinary`. That is why
adding the tag needs no version bump and no converter — historical segments already
say "ubinary" under the new reading.

That claim is about **bytes already on disk**, so it cannot be tested with bytes this
tree writes. Regenerating this fixture from the current encoder would produce a blob
whose tag was written deliberately as `0`, exercising the new writer twice and passing
just as happily if the historical claim were false.

`TestV3DtypeTagZeroLoadsAsUbinary` decodes this file and asserts it loads as ubinary,
with a float32-tagged blob built in the same run as the known-positive control — so a
reader that ignored the tag byte entirely could not pass.

Unlike the v2 fixture, this one **can** be recovered from history, because the encoder
that wrote it still exists there: extract the tree at a commit before the dtype tag
landed, build a segment through `Format.Build`, and keep `Encode()`'s bytes.

## `hnsw_v3_encoder_golden.seg`

The serialVersion-3 blob the CURRENT encoder emits for a fixed 256-node graph:
82,828 bytes, first byte `0x03`, `maxLevel` 1 with 265 neighbor runs over 256
nodes, `vecBytes` 32.

**Captured from `encodeGraphV3` before the emitter was restructured to write
through a sink. NEVER REGENERATE IT.**

This one differs in kind from the two above. Those are DECODE fixtures: they make
claims about bytes already on disk, and the tree can no longer produce them. This
one is an ENCODE fixture — the tree produces it on every run, and that is exactly
why it has to be checked in rather than generated. A test that built its
expectation from the current encoder would compare the encoder against itself and
stay green through any change to the stored layout.

The sixteen other `encodeGraphV3` call sites in this package all encode and then
decode, asserting a round trip. A round trip is preserved by any self-consistent
pair of emitter and reader, so none of them can see the layout move. This file is
the only thing that can.

The fixture is deliberately not minimal. Level assignment comes from the
builder's fixed seed rather than from the vectors, so node count is the only
lever on graph shape, and at 64 nodes the graph comes out single-layer with one
neighbor run per node — a blob whose layer-offset array and neighbor arena are
both degenerate. 256 nodes is the measured floor at which the upper layer is
populated, so the golden covers the sections a smaller one would leave empty.

To recover it: extract the tree at the commit before the sink restructure landed
(`git archive <sha> | tar -x -C <dir>`), build the same fixture through
`buildBinaryHNSWSerialDeterministic`, encode it with `encodeGraphV3`, and keep
the bytes. Do not re-derive it from a tree that carries the restructure — that
reproduces the expectation from the thing under test and throws the evidence
away.
