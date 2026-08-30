# veckernel — float32 dot kernels that tell you which one ran

A dependency-light float32 dot-product kernel for the graph vector index, with
per-architecture assembly tiers, runtime dispatch, and an active-tier report that
every test asserts on.

The only dependency beyond the standard library is `golang.org/x/sys/cpu`, for
feature detection.

## What it computes

Embeddings from the providers this project uses are pre-normalized, so cosine
similarity **is** the dot product. The package is that one operation in the two
shapes a graph traversal actually has:

```go
DotF32(a, b []float32) float32
DotF32Gather(dst, query, block []float32, dim int, ids []uint32)
```

`DotF32Gather` writes `dst[i] = dot(query, block[ids[i]*dim:][:dim])`. The ID
list over a flat block is a **measured** constraint, not a style preference:

| Alternative ABI | Why it loses |
| --- | --- |
| Contiguous-buffer batch (m adjacent vectors) | Graph neighbors are arbitrary scattered ids, so the caller must gather them into a staging buffer first. That copy measured **25.8 / 67.5 / 204.9 ns per distance** at dims 512 / 1024 / 2048 — several times the per-call overhead batching was meant to amortize. |
| Accessor closure `func(id uint32) []float32` | Assembly cannot call a Go closure, so the per-row loop moves back into Go and the assembly sees one row at a time. That forfeits four-row fusion, measured at **1.5–1.7x** — a bigger effect than the language or call-boundary differences it was first attributed to. |

## Tiers and dispatch

| Tier name | Arch | Status |
| --- | --- | --- |
| `arm64-neon` | arm64 | Hand-written Advanced SIMD, fused four-row gather. |
| `amd64-avx512` | amd64 | avo-generated, fused four-row gather. Needs `AVX` + `AVX512F` + `FMA3` — **not** `AVX512DQ`. |
| `amd64-avx2` | amd64 | avo-generated, fused four-row gather. Needs `AVX` + `AVX2` + `FMA3`. |
| `go-unroll4` | all | The portable four-way-unrolled reference. Always present, always supported, always last in preference order. |

The amd64 assembly is **generated, and the generator is not a build dependency**.
`avogen/` is a separate Go module holding an [avo](https://github.com/mmcloughlin/avo)
program; the `.s` and stub `.go` files it emits are committed, and the go tool
skips any directory carrying its own `go.mod`, so `go build ./...`, `go vet`,
`golangci-lint` and `go test` in `cmd/knowledge` never see avo and
`cmd/knowledge/go.mod` never carries it. Regenerate deliberately:

```sh
go generate -tags veckernel_avogen ./internal/searchengine/veckernel/
```

The `veckernel_avogen` tag is why regenerating is deliberate: `go generate ./...`
runs over the client module on every commit that touches it, and an untagged
directive would make avo a download that every such commit needs and would
re-emit and re-stage assembly nobody asked for.

```go
veckernel.Kernel()        // the tier actually running, e.g. "arm64-neon"
veckernel.Tiers()         // every compiled tier, with support status and reason
veckernel.ASMAvailable()  // is an assembly tier both compiled in and executable here
```

**Why the active tier is reported at all.** A kernel library that dispatches
silently cannot be distinguished from one that has quietly declined its SIMD
path — and a declined SIMD path returns correct, slow results forever without a
single test going red. The measurement work that preceded this package found
exactly that in a third-party library: its batched kernel silently fell back to
scalar at the widest contemplated dimension, at precisely this project's
neighbor count. It was visible only because the harness checked the dispatch
flag instead of believing it.

### Forcing a tier

```sh
VECKERNEL_FORCE=go-unroll4   go test ./...   # pin the portable reference
VECKERNEL_FORCE=arm64-neon   go test ./...   # pin the arm64 assembly tier
VECKERNEL_FORCE=amd64-avx512 go test ./...   # pin either amd64 assembly tier
VECKERNEL_FORCE=amd64-avx2   go test ./...
```

Four distinct outcomes, none of them a silent default:

| You asked for | You get |
| --- | --- |
| A tier this build has and this CPU runs | That tier. |
| A tier compiled in but unsupported by this CPU | `*UnsupportedTierError` naming the tier **and the hardware reason** — so a test can skip loudly instead of grading nothing. |
| A tier this package knows but this binary lacks — an arm64 tier on an amd64 build, or any assembly tier under `veckernel_noasm` | `*UnbuiltTierError`, saying **which of the two** it is: "this binary's assembly tiers are X, Y, so the requested one targets a different architecture", or "this binary carries no assembly tier at all". |
| Anything else | An error quoting the bad value and listing this build's vocabulary. Panics at init. |

The third row is a real distinction, not pedantry: an operator running the amd64
benchmark procedure on an arm64 laptop has picked the wrong machine, while an
operator who typed `avx512` has picked the wrong string, and an error that
conflates them sends both of them looking in the wrong place.

Forcing is not a debug convenience, it is how the tiers get tested. A CI
machine's capabilities are not a choice, so a suite that can only exercise
whatever tier the host happened to pick leaves the others ungraded everywhere.

### Removing assembly entirely

```sh
go build -tags veckernel_noasm ./...
```

Compile-time opt-out, distinct from the runtime pin: `noasm` takes the `.s`
files out of the binary. Use it for a build that cannot ship assembly, or to
bisect against a suspected assembly defect.

## Non-finite input policy

**NaN and Inf are not screened.** A per-element non-finite check would cost more
than the multiply-add it guards, on the hottest loop in the index. This is a
documented precondition, not a silent coercion — bad values propagate *visibly*
as NaN or Inf rather than being replaced with a plausible-looking number.
Validation belongs at ingest, once per vector, not once per distance.

Measured behavior, asserted on every tier through both entry points:

| Input | Result |
| --- | --- |
| Any NaN in either operand | NaN |
| A single `+Inf` / `-Inf` term | `+Inf` / `-Inf` |
| `+Inf` and `-Inf` both present | NaN |
| `Inf * 0` | NaN |
| True value exceeds float32 range | An infinity — never a wrapped or clamped finite number |

**One thing the package does not promise.** The tiers accumulate in different
groupings — four running sums in the reference, sixteen lanes on arm64,
thirty-two on AVX2, sixty-four on AVX-512 — so a partial sum can overflow in one
tier while the equivalent partial sum in another stays finite. The tiers are
therefore **not required to agree on inputs whose partial sums overflow**.

That began as a reservation and is now an **observation**. Three constructions
aimed at the gap were driven at authoring on arm64 and all three agreed. On
amd64 one of them diverges — dim 20, three large terms at indices 0, 4 and 8,
true total `1.7999999627338342e+38`:

| Tier | Result | Why |
| --- | --- | --- |
| `go-unroll4` | `+Inf` | all three indices are ≡ 0 mod 4, so all three land in accumulator 0 and the first two overflow it |
| `amd64-avx2` | `1.8e+38` | dim 20 is two 8-float passes, so indices 0 and 8 share a lane and cancel during accumulation |
| `amd64-avx512` | `1.8e+38` | dim 20 is one 16-float pass, so the fold's first step adds lane 8 onto lane 0 and cancels there |

The assembly tiers return the answer and the **reference** is the one that
overflows. `TestProbePartialOverflowDivergence` logs its verdict on every run,
so this stays a record of what the current tiers do rather than a claim measured
once on one architecture.

## Agreement and tolerance

The tiers do not produce identical bits and cannot be made to — float addition is
not associative, and a fused multiply-add rounds once where a separate multiply
and add round twice.

Agreement is graded **scale-relative at 1e-4**: error measured against
`sum|a_i*b_i|`, the magnitude the accumulator traverses. A literal relative
tolerance is *unmeetable* by any correct float32 implementation, because for two
random embedding vectors the dot cancels almost completely — the running sum
visits values of order `sum|a_i*b_i|` while the result sits near zero.

Two things pair with it:

- **Top-8 ranking agreement**, because ranking is what a search index ships and
  numeric agreement does not imply it.
- **A normal-range precondition.** Below the smallest normal float32, relative
  precision decays to nothing and the bound does not apply. Fuzzing found this:
  at length two with a subnormal scale the tiers differed by 1.9e-4 while both
  were correct.

The scale-relative form has one **known blind spot**, pinned by a test rather
than left to be rediscovered: on strongly-canceling data it cannot see a
*uniform* multiplicative error. A uniform scaling preserves ranking exactly;
non-uniform errors — the ones that reorder — are caught by the ranking gate.

## Running the tests

```sh
# The full correctness suite. Runs as part of the client module's tests.
go test ./cmd/knowledge/internal/searchengine/veckernel/

# Same suite pinned to the portable reference, on any architecture.
VECKERNEL_FORCE=go-unroll4 go test ./cmd/knowledge/internal/searchengine/veckernel/

# Same suite with assembly compiled out entirely.
go test -tags veckernel_noasm ./cmd/knowledge/internal/searchengine/veckernel/

# Race detector.
go test -race ./cmd/knowledge/internal/searchengine/veckernel/
```

### On a machine you do not have

Every tier's correctness is graded by a **cross-compiled test binary**, so an
amd64 tier can be exercised from an arm64 workstation without a Go toolchain on
the target:

```sh
GOOS=linux GOARCH=amd64 go test -c -o /tmp/veckernel.test \
  ./cmd/knowledge/internal/searchengine/veckernel/
cp -R cmd/knowledge/internal/searchengine/veckernel/testdata /tmp/
# then, on the target, from the directory holding testdata/:
./veckernel.test
VECKERNEL_FORCE=amd64-avx2 ./veckernel.test
```

**Copy `testdata/` next to the binary.** The committed fuzz corpus is replayed
from `testdata/fuzz/FuzzDotAgreement/` relative to the working directory; run the
binary somewhere without it and the regression seed is silently not replayed —
a green run that skipped the one input that ever caught a real defect. Confirm
it ran: `./veckernel.test -test.run FuzzDotAgreement -test.v` names the corpus
entry (`FuzzDotAgreement/098b1517bca1182b`) alongside the in-code seeds.

**The fuzzing ENGINE does not run from a compiled binary** — `-test.fuzz` needs
the go command to build and coordinate workers. A bare test binary replays the
corpus and nothing more. To fuzz a tier on a machine you do not develop on,
install Go there and run `go test -fuzz` against the source.

The suite covers, each with a known-positive control proving the gate fires:

| Class | What it does |
| --- | --- |
| Agreement | Every tier against a float64 oracle and against the reference; top-8 ranking agreement on seeded corpora. |
| Tail exhaustion | Every dim 1..300 plus every production width and its neighbors, so every remainder path executes. Dim 1024 is a multiple of every loop stride and executes **no** remainder instruction — testing only production widths leaves them all unrun. |
| Value domain | Zeros, hand-pinned exact answers, near-total cancellation, subnormals, non-finite inputs, and sub-slice alignment at all 16 byte offsets. |
| Dispatch | The active tier is asserted per architecture; forcing works, reports, and releases; every resolver branch is driven including ones for tiers this machine lacks. |
| Fuzz | Go native fuzzing over lengths and values with the float64 oracle. |

### Fuzzing

```sh
go test -run '^$' -fuzz FuzzDotAgreement -fuzztime 180s \
  ./cmd/knowledge/internal/searchengine/veckernel/
```

`testdata/fuzz/FuzzDotAgreement/` holds the committed corpus, replayed on every
ordinary `go test` run. The entry there is a **regression seed**: it is the input
that caught a real defect in this suite's own preconditions — a length-two case
with a subnormal scale where the tiers legitimately differ by 1.9e-4. Do not
delete it.

**Fuzz each tier separately.** The body grades whatever `testArms()` returns, so
a run only exercises the tiers this host can execute and dispatch has selected;
pin the others with `VECKERNEL_FORCE`. The amd64 tiers were fuzzed on the
benchmark instance at 120s each: `amd64-avx512` 14.6M executions, `amd64-avx2`
20.1M, both clean. Fuzzing is also the one class a cross-compiled test binary
cannot carry — see [On a machine you do not have](#on-a-machine-you-do-not-have).

## Benchmarks and pinned floors

```sh
# Standing benchmark suite. ALWAYS pass an explicit -benchtime=Nx (see below).
go test -count=1 -run '^$' -bench . -benchtime 3000x \
  ./cmd/knowledge/internal/searchengine/veckernel/

# The floor gate: fails if any tier regresses past 2x its pinned floor.
VECKERNEL_PERF=1 go test -count=1 \
  -run 'TestPerfFloors|TestPerfFloorGate|TestDispatchPreference' \
  ./cmd/knowledge/internal/searchengine/veckernel/
```

```sh
# Harvest this host's pins: prints pinTable rows with their measured spread and
# the per-cell tolerance derived from it.
VECKERNEL_PERF_HARVEST=1 go test -count=1 -run TestPerfHarvestPins -v \
  ./cmd/knowledge/internal/searchengine/veckernel/
```

**`-count=1` IS NOT OPTIONAL ON ANY TIMING COMMAND.** Go caches test results, and
a cached result is replayed without executing anything — so a timing gate run
twice reports the second time in nanoseconds it never measured. Four consecutive
"runs" of the floor gate were observed returning byte-identical output down to
the 0.1 ns, because only the first one ran. The test cannot detect this: a cached
run does not execute, so there is no code in it to notice. `-count=1` at the call
site is the only defense, which is why every timing command in this file carries
it and why `TestDocumentedTimingCommandsDisableTheTestCache` fails the suite if
one loses it.

The floor gate is env-gated rather than build-tag-gated on purpose: a file behind
a build tag is a file the linter never compiles unless every lint invocation is
told about the tag, and a pass that silently skips files reports success having
checked nothing. Its skip message names the variable and what goes unchecked.

**The traverse number depends on how long you walk.** A random walk over the
128 MiB corpus revisits nodes, so a longer walk has warmed more of its own
working set and each subsequent distance looks cheaper. Same machine, same
kernel, NEON at dim 256: **23.9 ns/distance at `-benchtime=3000x`, 9.9
ns/distance** when the framework auto-scaled the walk into six figures. Neither
is wrong; they measure different cache warmth. The floor gate therefore pins its
own protocol — a fixed 2000 hops, chosen to sit at query scale — instead of
using the benchmark framework's count.

## The pin table is keyed by MACHINE CLASS

`pinTable` is not keyed by `GOARCH`. It is keyed by a **machine class** the
running process can determine about itself:

| Class | Resolves when | Measured on |
| --- | --- | --- |
| `arm64` | `GOARCH=arm64` | Apple M4 Max |
| `amd64-avx512-capable` | `GOARCH=amd64` and CPUID reports AVX512F | GCE `c3-standard-8`, Intel Xeon Platinum 8481C (Sapphire Rapids) |
| `amd64-no-avx512` | `GOARCH=amd64` and it does not | GCE `n2d-standard-8`, AMD EPYC 7B13 (Milan) |

**A class the process cannot resolve is a class that inherits.** That is the
whole reason the key is a capability rather than an instance name: a key the
binary cannot determine would send every host to whichever row the lookup
happened to match, which is the silent borrowing a per-class table exists to
stop, dressed up as a lookup. `pinFor` matches the class **exactly** and there is
no nearest-class fallback — a host whose class has no rows gets **no floor**, and
`TestPerfFloorsTraverse` says so by name rather than guarding against another
machine's numbers.

The two amd64 classes are not cosmetic. They disagree materially: see
[the wide-dim question](#the-wide-dim-question-is-machine-dependent).

**What a capability class does not buy.** It is coarser than a silicon class. An
Intel client part with AVX-512 fused off resolves to `amd64-no-avx512` and reads
floors measured on an AMD Milan; a Graviton resolves to `arm64` and reads floors
measured on an M4 Max. Every pin's `Machine` field names the silicon its numbers
came from, so the coarseness is visible where the number is used. When a part
turns up that its class genuinely misprices, the fix is a new class with its own
resolution rule and its own rows — never a loosened tolerance.

All three classes were re-harvested 2026-08-25 under one protocol: **corpus sized
to 4x the host's largest reported cache**, 2000 hops, **five runs per cell** with
the minimum pinned and `max/min` recorded as that cell's spread, and a **per-cell
tolerance derived** from that spread. Pins on the cloud classes are the best of
two independent harvests.

Each table shows the PINNED floor, that cell's own measured harvest spread, the
tolerance derived from it, and a later verification pass measured against it. The
verification column is what says the floor is a floor.

### Measured: `arm64` — Apple M4 Max

Corpus 128 MiB (the floor; 8x this host's 16 MiB largest *reported* cache).

| Tier | Dim | pin | spread | tol | verify | ratio | micro |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `arm64-neon` | 256 | 11.8 | 1.12x | 1.40x | 12.9 | 1.09x | 16.3 |
| `arm64-neon` | 512 | 23.6 | 1.07x | 1.34x | 23.9 | 1.01x | 26.9 |
| `arm64-neon` | 1024 | 51.4 | 1.04x | 1.30x | 51.0 | 0.99x | 51.3 |
| `arm64-neon` | 2048 | 115.4 | 1.02x | 1.30x | 115.8 | 1.00x | 95.7 |
| `go-unroll4` | 256 | 65.8 | 1.07x | 1.34x | 65.3 | 0.99x | 47.0 |
| `go-unroll4` | 512 | 122.6 | 1.05x | 1.31x | 125.7 | 1.02x | 99.6 |
| `go-unroll4` | 1024 | 231.6 | 1.04x | 1.30x | 237.0 | 1.02x | 217.4 |
| `go-unroll4` | 2048 | 490.0 | 1.01x | 1.30x | 491.5 | 1.00x | 445.9 |

**A loaded machine will fail this gate, and the reference row is how you tell.**
Running the verification while a cloud benchmark was driving `gcloud` in the
background put every cell 1.29–1.50x over its pin — including `go-unroll4`, which
is pure portable Go and was not touched. A kernel change cannot slow down a
kernel it did not modify, so a uniform shift across both tiers is the machine, not
the code. Quiet, the same cells read 0.99–1.09x.

### Measured: `amd64-avx512-capable` — Intel Xeon Platinum 8481C (Sapphire Rapids)

GCE `c3-standard-8`. Corpus **420 MiB** — 4x this part's 105 MiB L3.

| Tier | Dim | pin | spread | tol | verify | ratio | micro |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `amd64-avx512` | 256 | 13.3 | 1.11x | 1.39x | 13.3 | 1.00x | 9.8 |
| `amd64-avx512` | 512 | 24.4 | 1.14x | 1.43x | 24.4 | 1.00x | 16.0 |
| `amd64-avx512` | 1024 | 64.0 | 1.20x | 1.50x | 68.2 | 1.07x | 29.6 |
| `amd64-avx512` | 2048 | 90.3 | 1.11x | 1.39x | 93.4 | 1.03x | 56.8 |
| `amd64-avx2` | 256 | 14.9 | 1.08x | 1.36x | 14.8 | 0.99x | 12.7 |
| `amd64-avx2` | 512 | 27.5 | 1.14x | 1.43x | 28.2 | 1.02x | 22.2 |
| `amd64-avx2` | 1024 | 74.7 | 1.14x | 1.43x | 77.3 | 1.04x | 40.6 |
| `amd64-avx2` | 2048 | 108.7 | 1.09x | 1.37x | 111.1 | 1.02x | 78.5 |
| `go-unroll4` | 256 | 102.8 | 1.03x | 1.30x | 99.9 | 0.97x | 95.1 |
| `go-unroll4` | 512 | 202.7 | 1.03x | 1.30x | 206.9 | 1.02x | 191.0 |
| `go-unroll4` | 1024 | 427.8 | 1.01x | 1.30x | 431.2 | 1.01x | 381.9 |
| `go-unroll4` | 2048 | 788.0 | 1.00x | 1.30x | 790.2 | 1.00x | 753.7 |

### Measured: `amd64-no-avx512` — AMD EPYC 7B13 (Milan)

GCE `n2d-standard-8`, `--min-cpu-platform="AMD Milan"`. `/proc/cpuinfo` shows
`avx avx2` and **no `avx512` flag of any kind**, so this class's AVX2 numbers
carry no forced-tier approximation. Corpus 128 MiB — 4x this part's 32 MiB L3.

| Tier | Dim | pin | spread | tol | verify | ratio | micro |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `amd64-avx2` | 256 | 21.0 | 1.21x | 1.52x | 19.0 | 0.90x | 20.6 |
| `amd64-avx2` | 512 | 32.4 | 1.06x | 1.32x | 33.3 | 1.03x | 31.9 |
| `amd64-avx2` | 1024 | 85.3 | 1.03x | 1.30x | 69.6 | 0.82x | 50.3 |
| `amd64-avx2` | 2048 | 155.4 | 1.34x | 1.68x | 151.1 | 0.97x | 92.3 |
| `go-unroll4` | 256 | 128.1 | 1.15x | 1.43x | 118.1 | 0.92x | 95.8 |
| `go-unroll4` | 512 | 204.8 | 1.02x | 1.30x | 200.5 | 0.98x | 182.1 |
| `go-unroll4` | 1024 | 379.7 | 1.04x | 1.30x | 376.1 | 0.99x | 355.8 |
| `go-unroll4` | 2048 | 761.5 | 1.06x | 1.33x | 741.2 | 0.97x | 699.9 |

This class is the noisiest of the three — the `amd64-avx2` dim 1024 cell verified
at 0.82x, the widest gap in the table. That is a shared-tenancy n2d, and the
two-sided gate's stale-floor arm fires below 0.5x, so 0.82x is disclosed rather
than alarming. `amd64-avx512` has **no row here and must never gain one**: that
tier cannot execute on this part, and
`TestEveryClassesPinsAreCompleteAndMeasured` asserts the absence.

### Dispatch preference: AVX-512, re-derived out of cache

`amd64PreferAVX512` is `true`. Verified on `c3-standard-8`, both tiers re-timed in
the same pass:

| Dim | AVX-512 | AVX2 | AVX-512 faster by |
| ---: | ---: | ---: | ---: |
| 256 | 13.2 | 14.8 | 11% |
| 512 | 24.5 | 28.1 | 13% |
| 1024 | 70.1 | 73.5 | 5% |
| 2048 | 91.4 | 107.2 | 15% |

The lead is steady across widths and does not collapse. An earlier measurement on
a 128 MiB corpus — 1.22x this part's L3 — reported 39.2 ns at dim 1024, an implied
~104 GB/s per core, which is a cache figure wearing a kernel's name, and made the
lead look like it fell to 5% at dim 2048. Sizing the corpus against the LLC
removed that artifact. The preference never flipped, but it was being read off
cells measuring the wrong thing.

## What was built, and what the measurements said

### Software prefetch: capped, and worth about 16% at one width

All three fused gathers now prefetch the NEXT group's four rows while scoring the
current one — `PRFM PLDL1KEEP` on arm64, `PREFETCHT0` on amd64 — capped at **one
kilobyte per row** and gated to `dim >= 256` so the cursor never runs past the row
it was aimed at.

**The cap is the mechanism, not caution.** Prefetching a candidate row IN FULL is
actively harmful at production widths: measured, one group ahead at full row
length cost **+12.7% at dim 1024 and +35.4% at dim 2048** against no prefetch.

Measured in-package on arm64, prefetch on against off: **−15.8% at dim 256**,
and +1–3% at 512/1024/2048 — inside those cells' own spreads, but consistently on
the slow side. So it is a real win at the narrow end and a wash-to-slight-cost
above it. The honest summary is one width.

### Twelve chains in the fused gather: measured, rejected

The shipped gather carries two accumulators per row (eight chains). A
twelve-chain variant — three per row, 31 of 32 vector registers — was built and
A/B'd against it to settle a recorded prediction of no gain:

| Dim | 8-chain | 12-chain | delta | cache-hot delta |
| ---: | ---: | ---: | ---: | ---: |
| 256 | 12.9 | 14.2 | +10.3% | +13.7% |
| 512 | 25.4 | 29.0 | +14.0% | +7.6% |
| 1024 | 52.3 | 53.1 | +1.6% | +1.6% |
| 2048 | 117.9 | 114.3 | −3.1% | −3.1% |

**Twelve chains is worse at three of four production widths** and wins ~3% only
at dim 2048, reproducibly, in both the traverse and the cache-hot control. It is
NOT shipped. The prediction of "no gain" held at 256/512/1024 — in fact more
chains actively hurt there, which says the eight-chain kernel is not chain-starved
at all.

### Row-stride aliasing: null result on both architectures

Power-of-two row strides could in principle map every candidate in a hop onto one
cache set. Probed by comparing a packed corpus against one with rows spaced
`+64B`, holding node count, neighbor graph AND row contents fixed so both layouts
walk identically:

| Class | worst cell | verdict |
| --- | --- | --- |
| `arm64` | +7.4% (one NEON cell; reference at same width −1.8%) | no effect |
| `amd64-avx512-capable` | +15.9% (padding *worse*) | no effect |

**No cell showed padding winning meaningfully on either architecture.** Nothing
to escalate to the index format.

Two confounds had to be removed before this probe said anything. Sizing both
layouts to equal BYTES gave them different node counts; and filling the block as
one random stream made row contents depend on the stride, so the two layouts
scored differently and walked different paths. Both produced swings up to +366%
that were pure artifact. The probe now asserts the two layouts reach the same
terminal node before it will compare their timings.

### The id-remainder cliff: a walk artifact, not a tail cost

Scoring 63 candidates instead of 64 costs **+78%** per distance on the
AVX-512-capable class. That looked like the per-row tail — the leftover rows go
through the scalar dot, one call each — and was recorded as such. Two controls on
the hardware that shows it proved otherwise:

- **Replacing every scalar-dot tail call** with a single fused four-row call (last
  row repeated, discarded, graded against the oracle before timing) moved the
  number by **0.4%**. The tail kernel is not the cost.
- **Holding the walk fixed** — every id count visiting the same precomputed nodes
  in the same order — collapses it to **+0.2%**: 60/63/64 ids cost 259.7 / 260.2 /
  257.7 ns.

The mechanism is the **argmax moving the walk**: 63 candidates elect a different
winner, the traversal takes a different path, and that path has worse locality. A
memory-bound assembly tier pays the whole difference; the compute-bound reference
cannot reach the memory system hard enough to notice — which is why its silence
looked like evidence the walk had not changed, and was not.

**No kernel fix is warranted**, and the 2-row/3-row fused kernels contemplated for
it would recover approximately nothing. `idRemainderPins` is kept as a stable
tripwire (spreads 1.00–1.04) but it is **not** a measurement of the tail path.

**Read the traverse simulation as a pessimistic adversarial model.** A real beam
search keeps a candidate heap and a visited set, revisits nodes, and expands in a
far more locality-friendly order than argmax-of-the-last-hop. This walk does none
of that deliberately, so a kernel that looks good here is not relying on locality
a real query may not supply. It is not a claim about what production costs.

## GCE benchmark procedure (operator)

The amd64 numbers come from **on-demand** instances. Each is created for a run and
destroyed after it — never left standing. Everything below was executed
2026-08-25 across five instances; the corrections it carries are things earlier
versions of this section got wrong, found by running them.

### Networking: the DEV shared VPC, and nothing of ours to clean up

The bench box attaches to the **dev environment's existing network** in the
shared-VPC host project `fulminate-network`:

```
--subnet projects/fulminate-network/regions/us-central1/subnetworks/executor-subnet
```

**Why that subnet is identifiably dev**, confirmed by usage rather than by
guessing at the name: it carries 19 running instances all named `dev-<uuid>`, it
sits on the `executor` network, and there is a separate `executor-prod` network
with its own `executor-prod-subnet` for the production counterpart. The host
project's other subnet, `main-subnet`, hosts the `main-us-central1` GKE cluster
and is shared infrastructure, not dev. `fulminate-services` is already an
attached service project of this shared VPC, so no IAM change is needed.

**The network is not ours and is never deleted.** Only two things are created and
torn down per round: the instance, and one SSH ingress rule.

**The firewall rule is still per-round even though the network persists.** It is
created before the instance and deleted by the same unconditional trap:

```sh
gcloud compute firewall-rules create veckernel-bench-ssh \
  --project fulminate-network --network executor \
  --direction INGRESS --priority 1000 --action allow --rules tcp:22 \
  --source-ranges "$(curl -s https://checkip.amazonaws.com)/32" \
  --target-tags veckernel-bench
```

`--target-tags` is what makes this safe on a shared network: the rule applies
**only** to instances carrying `veckernel-bench`, so no `fulminate-dev` or
`fulminate-executor` workload can be reached through it. Combined with the `/32`
source range it is reachable from one operator machine and one bench box. **Never
leave a standing ingress rule** — delete it with the instance even though the
network survives.

### Prerequisites earlier attempts discovered

**Zones stock out.** `c3-standard-8` was `STOCKOUT` in `us-central1-a` and
`us-central1-b` and available in `us-central1-c` on one run, and available in
`-a` on another. Search zones rather than hard-coding one.

**The box needs no Go toolchain** for correctness or benchmarks. Push a
cross-compiled test binary (see [On a machine you do not have](#on-a-machine-you-do-not-have))
plus `testdata/`. It only needs Go for **fuzzing**, which cannot run from a
compiled binary.

**Record the silicon you actually got.** `grep -m1 "model name" /proc/cpuinfo` on
the booted instance, not the machine family — the family is a purchasing
category, the part number is what executed the code. It goes into the `Machine`
field of every pin the run produces, and the class the run fills is decided by
whether `/proc/cpuinfo` shows an `avx512` flag at all.

### Instance class

| Goal | Machine type | Fills class |
| --- | --- | --- |
| Both amd64 tiers, and the dispatch preference between them | `c3-standard-8` (Sapphire Rapids) | `amd64-avx512-capable` |
| Same, one generation on | `c4-standard-8` (Emerald Rapids) | `amd64-avx512-capable` |
| Same, older | `n2-standard-8` **with `--min-cpu-platform="Intel Ice Lake"`** — unpinned `n2` can land on Cascade Lake or on AMD Rome, so "n2" alone says nothing about the silicon | `amd64-avx512-capable` |
| **The AVX2-only mainstream**, and the only way to measure AVX2 without a forced-tier approximation | `n2d-standard-8` **with `--min-cpu-platform="AMD Milan"`** | `amd64-no-avx512` |

**Both amd64 classes are required.** They disagree materially — see
[the wide-dim question](#the-wide-dim-question-is-machine-dependent) — and the
AVX2 rows in the AVX-512-capable class carry a caveat the AVX2-only class does
not: forcing AVX2 on a part that also has AVX-512 runs it in that part's
frequency bin, not the bin a non-AVX-512 part ships in.

### The run

Deterministic names so a leak is findable. Teardown is a `trap` and runs
**unconditionally**, including when the benchmark fails — an orphaned benchmark
box burning idle dollars is the failure this is designed out of.

```sh
#!/usr/bin/env bash
set -uo pipefail            # NOT -e: every step reports its own status and the
                            # trap must still run if one of them fails

INSTANCE=veckernel-bench-amd64
FW=veckernel-bench-ssh
TAG=veckernel-bench
HOST_PROJECT=fulminate-network
SUBNET=projects/fulminate-network/regions/us-central1/subnetworks/executor-subnet
MYIP="$(curl -s https://checkip.amazonaws.com)/32"

cleanup() {
  gcloud compute instances delete "$INSTANCE" --zone "$ZONE" --quiet
  gcloud compute firewall-rules delete "$FW" --project "$HOST_PROJECT" --quiet
  # The executor network and executor-subnet are the dev environment's. NOT OURS.
}
trap cleanup EXIT INT TERM

gcloud compute firewall-rules create "$FW" --project "$HOST_PROJECT" \
  --network executor --direction INGRESS --priority 1000 --action allow \
  --rules tcp:22 --source-ranges "$MYIP" --target-tags "$TAG" --quiet

for Z in us-central1-a us-central1-b us-central1-c us-central1-f; do
  if gcloud compute instances create "$INSTANCE" --zone "$Z" \
       --machine-type c3-standard-8 --subnet "$SUBNET" --tags "$TAG" \
       --image-family debian-12 --image-project debian-cloud \
       --boot-disk-size 20GB --quiet; then ZONE="$Z"; break; fi
done

gcloud compute scp --zone "$ZONE" --quiet /tmp/veckernel.test "$INSTANCE":~/
gcloud compute scp --zone "$ZONE" --quiet --recurse /tmp/testdata "$INSTANCE":~/

# CORRECTNESS FIRST, per tier and through both seams. A benchmark of a wrong
# kernel is a number about nothing.
for TIER in "" amd64-avx512 amd64-avx2 go-unroll4; do
  gcloud compute ssh "$INSTANCE" --zone "$ZONE" --quiet \
    --command "VECKERNEL_FORCE=$TIER ./veckernel.test"
done

# THEN the pins, from the floor gate's own protocol.
gcloud compute ssh "$INSTANCE" --zone "$ZONE" --quiet --command \
  "VECKERNEL_PERF_HARVEST=1 ./veckernel.test -test.run TestPerfHarvestPins -test.v"
```

### Prove the gate fires on that silicon

`TestPerArmOracleGateFiresOnThatArmsOwnKernel` and
`TestPerArmGatherGateFiresOnThatArmsOwnKernel` break **each tier's own kernel**
and require the gate to reject it. They run as part of the suite above, and their
log lines name every tier they covered and every tier they could not.

For a stronger proof that a genuine assembly defect is caught, build a scratch
copy with the kernels deliberately broken and run it on the box — it must go
**red, naming the amd64 tiers that machine can execute**:

```sh
cp -R cmd/knowledge/internal/searchengine/veckernel /tmp/vkred/veckernel
rm -rf /tmp/vkred/veckernel/avogen
printf 'module vkred\n\ngo 1.26.4\n' > /tmp/vkred/go.mod
(cd /tmp/vkred && GOWORK=off GOFLAGS=-mod=mod go mod tidy)
# turn each `JE <label>_reduce` into `JMP <label>_reduce` in dot_avx_amd64.s,
# which makes both amd64 tiers skip their scalar remainder entirely
(cd /tmp/vkred && GOWORK=off GOOS=linux GOARCH=amd64 go test -c -o /tmp/veckernel.tailbroken.test ./veckernel)
```

### Check for leftovers

Part of the procedure, not an afterthought — run it after every session. Two
things can leak, and **the network is not one of them** because the procedure
never creates one:

```sh
gcloud compute instances list \
  --filter="name~'^veckernel-bench-'" --format=json
gcloud compute firewall-rules list --project fulminate-network \
  --filter="name~'^veckernel-bench-'" --format=json
```

`[]` from both is the expected result. **Use `--format=json`.** With the default
table format and a filter matching nothing, `gcloud` prints either a format-help
blurb or nothing at all, and an operator reading blank output as "nothing was
left behind" is guessing rather than checking.

**Then assert the host project is INTACT**, which an absence check cannot do — it
would pass just as happily if the run had deleted something of the dev
environment's. Snapshot before, diff after:

```sh
# before
gcloud compute networks list --project fulminate-network --format="value(name)" > net.before
gcloud compute networks subnets list --project fulminate-network --format="value(name,region)" > sub.before
gcloud compute firewall-rules list --project fulminate-network --format="value(name)" > fw.before
# after (excluding our own ephemeral rule)
... | grep -v '^veckernel-bench-' > fw.after
diff net.before net.after && diff sub.before sub.after && diff fw.before fw.after
```

Expected: 3 networks, 7 subnets and 24 firewall rules, unchanged.

### After the run

1. Fill the class's rows in `pinTable` (`pins.go`) from `TestPerfHarvestPins`,
   which prints them as literal table rows keyed by the class it resolved.
   Clear `Unmeasured`; set `Machine` from `/proc/cpuinfo`.
2. If the class supports both amd64 tiers, set `amd64PreferAVX512` in
   `kernel_amd64.go` from which tier **measured faster in the traverse
   benchmark**, per `dispatchPolicy`. Where classes disagree, that constant is
   the thing that has to become per-class — do not average them.
3. Re-run **on the same class** with the filled table and `VECKERNEL_PERF=1`, so
   `TestPerfFloorsTraverse`, `TestPerfFloorGateRejectsASlowKernel` and
   `TestDispatchPreferenceIsMeasured` grade against real floors. Filling pins
   without that second pass leaves the floor gate itself unexercised on the class
   it was just written for.
