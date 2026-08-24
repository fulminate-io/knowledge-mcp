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
