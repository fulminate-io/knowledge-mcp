// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/veckernel"
)

// ErrMixedVectorWidth reports a build batch whose vectors are not all the same
// width. Callers match it with errors.Is; the wrapped message names the widths
// and the offending id.
var ErrMixedVectorWidth = errors.New("hnsw: batch mixes vector widths")

// ErrMixedVectorDtype reports a merge whose constituents do not all carry the
// same vector dtype. Callers match it with errors.Is; the wrapped message names
// both tags and the offending constituent's index.
var ErrMixedVectorDtype = errors.New("hnsw: batch mixes vector dtypes")

// batchDtype returns the single dtype shared by every constituent of a merge,
// and REFUSES a set that mixes them.
//
// IT IS THE DTYPE HALF OF batchVecBytes, and exists because the width half alone
// was not enough. Merge derived its width from the survivors but passed a
// hardcoded ubinary dtype, so consolidating two float32 segments produced a
// segment whose vectors were byte-for-byte correct and whose METRIC was wrong:
// float bit patterns ranked by Hamming distance. Nothing about that fails — it
// returns confident, wrong neighbors — which is precisely why the pair has to be
// derived together rather than one half at a time.
//
// THERE IS NO CORRECT DTYPE TO COERCE A MIXED SET TO. The two encodings are
// different readings of the same bytes, so either choice silently reinterprets
// one constituent's vectors. Refusing names the condition at the point the
// mistake enters, like the width refusal beside it.
//
// AN EMPTY SET IS NOT AN ERROR, matching batchVecBytes: an empty merge yields an
// empty segment, whose dtype is unconstrained, so the default stands in.
func batchDtype(segs []*hnswSegment) (byte, error) {
	if len(segs) == 0 {
		return dtypeUbinary, nil
	}
	dtype := segs[0].graph.dtype
	for i, s := range segs[1:] {
		if s.graph.dtype != dtype {
			return 0, fmt.Errorf("%w: constituent %d has dtype %d but the batch is dtype %d",
				ErrMixedVectorDtype, i+1, s.graph.dtype, dtype)
		}
	}
	return dtype, nil
}

// batchBuildDtype returns the single dtype tag shared by every item in a BUILD
// batch, and REFUSES a batch that mixes them.
//
// IT IS batchDtype FOR THE BUILD SIDE, and the pair exists because the two sides
// are handed different things: a merge is handed sealed segments, each of which
// already carries a tag, while a build is handed documents, each of which
// carries the representation its producer recorded. Same question, same refusal,
// two operand types.
//
// IT REPLACES A HARD-CODED dtypeUbinary ARGUMENT, and what that argument did was
// invisible rather than loud. Build derived its WIDTH from the documents and
// then told the builder the vectors were ubinary regardless, so correct float32
// bytes were sealed into a float32-sized segment tagged ubinary and ranked by
// Hamming distance over IEEE bit patterns: every byte preserved, every length
// check quiet, and the ordering wrong.
//
// A MIXED BATCH IS REFUSED FOR THE SAME REASON A MIXED WIDTH IS. The two
// encodings are different readings of the same bytes, so sealing one tag over a
// mixed batch silently reinterprets somebody's vectors, and there is no correct
// tag to coerce to. Refusing names the condition where the mistake enters.
//
// AN EMPTY BATCH IS NOT AN ERROR, matching batchVecBytes and batchDtype: an
// empty build yields an empty segment whose dtype is unconstrained.
func batchBuildDtype(items []binaryBuildItem) (byte, error) {
	if len(items) == 0 {
		return dtypeUbinary, nil
	}
	first, err := dtypeTagFor(items[0].dtype)
	if err != nil {
		return 0, fmt.Errorf("id %q: %w", items[0].id, err)
	}
	for _, it := range items[1:] {
		tag, terr := dtypeTagFor(it.dtype)
		if terr != nil {
			return 0, fmt.Errorf("id %q: %w", it.id, terr)
		}
		if tag != first {
			return 0, fmt.Errorf("%w: id %q is dtype %d but the batch is dtype %d",
				ErrMixedVectorDtype, it.id, tag, first)
		}
	}
	return first, nil
}

// dtypeTagFor maps a document's dtype NAME onto this format's on-disk tag.
//
// THE EMPTY NAME IS UBINARY, which is the same statement the format's tag-0
// reading already makes about every segment written before the tag existed —
// see the dtype tag constants in serial.go. It is a translation of an
// established convention, not a fallback for an unrecognized value: a name this
// format does not know is REFUSED below rather than read as ubinary, because
// answering ubinary for an unknown representation would rank its vectors by
// Hamming distance and return confident wrong neighbors.
func dtypeTagFor(name string) (byte, error) {
	switch name {
	case "", searchengine.DtypeUbinary:
		return dtypeUbinary, nil
	case searchengine.DtypeFloat32:
		return dtypeFloat32, nil
	default:
		return 0, fmt.Errorf("hnsw: unknown vector dtype %q (want %q or %q)",
			name, searchengine.DtypeUbinary, searchengine.DtypeFloat32)
	}
}

// batchVecBytes returns the single vector width shared by every item in a build
// batch, and REFUSES a batch that mixes widths.
//
// THIS REPLACES A MEASURED SILENT CORRUPTION, which is why it errors instead of
// coercing. Build and Merge previously passed a fixed 32-byte width regardless
// of what the documents actually carried. The width-threading research measured the
// consequence by execution: at a 16-byte width the build SUCCEEDS WITH NO ERROR
// and stores id a's vector as a's first 16 bytes concatenated with b's, id b's
// as c's bytes plus zeros, and id c's as all zeros; at 64 and 128 bytes it
// panics inside the distance function instead. Corruption that no test observes
// is the worse of those two, and both are the caller's width being ignored.
//
// THERE IS NO CORRECT WIDTH TO COERCE A MIXED BATCH TO — every choice truncates
// or zero-pads somebody's vector into a different point in space — so this is a
// refusal rather than a fallback: it names the condition and what was dropped,
// at the point the mistake enters.
//
// AN EMPTY BATCH IS NOT AN ERROR. Build's contract is that an all-empty batch
// yields an empty, searchable, zero-hit segment, so the width is unconstrained
// and the package default stands in. This is also the one place that default is
// still named — format.go no longer mentions it, because a width the format
// picks for itself is exactly the bug above.
func batchVecBytes(items []binaryBuildItem) (int, error) {
	if len(items) == 0 {
		return defaultVecBytes, nil
	}
	width := len(items[0].vec)
	for _, it := range items[1:] {
		if len(it.vec) != width {
			return 0, fmt.Errorf("%w: id %q is %d bytes but the batch is %d bytes",
				ErrMixedVectorWidth, it.id, len(it.vec), width)
		}
	}
	return width, nil
}

// dtype.go holds the two things that differ between a ubinary segment and a
// float32 one: WHICH METRIC measures distance, and HOW a distance becomes a
// score. Everything else about the traversal is shared — see traverse.go.

// dtypeFromHeader resolves a blob's dtype from its header, validating the tag
// and its agreement with the serial version.
//
// IT LIVES HERE RATHER THAN INLINE IN THE READER because dtype and version are
// one fact recorded twice, and this file is the single home of what differs by
// dtype. Keeping the two checks adjacent is also what makes the second one
// obviously necessary: without it the version byte would be decorative, and the
// protection older readers rely on — that a float32 blob never claims a version
// they accept — would hold only by the writer's good behavior.
//
// AN UNRECOGNIZED TAG IS REFUSED, NEVER COERCED. Defaulting it to ubinary would
// reinterpret float32 vectors as bit patterns and rank them by Hamming distance
// — a silently wrong ordering rather than a failure, which is the exact class of
// degradation this format's rejections exist to prevent. Both refusals carry the
// same rebuild remedy as the version and CRC ones, so all of them route into the
// heal path together.
func dtypeFromHeader(tag, version byte) (byte, error) {
	if tag != dtypeUbinary && tag != dtypeFloat32 {
		return 0, fmt.Errorf(
			"hnsw open: unsupported vector dtype tag %d (want %d for ubinary or %d for float32); this segment was written by a newer build and has no converter — rebuild it from source",
			tag, dtypeUbinary, dtypeFloat32)
	}
	if want := versionForDtype(tag); version != want {
		return 0, fmt.Errorf(
			"hnsw open: serial version %d disagrees with dtype tag %d, which is written at version %d; this segment is inconsistent and has no converter — rebuild it from source",
			version, tag, want)
	}
	return tag, nil
}

// preparedQuery is one search's query, converted ONCE for the segment it is
// about to walk. The traversal carries this rather than raw bytes.
//
// THE FLOAT32 ARM IS A COPY, AND THAT IS THE WHOLE POINT. The obvious
// implementation — a float32 VIEW over the caller's query buffer, the same
// unsafe.Slice idiom f32sAt uses for the block — is unsound here and was
// measured to be: a 16-byte query against a 256-byte float32 segment produced a
// 64-element view over a 16-byte allocation and read 240 bytes past its end,
// silently, returning plausible neighbors. Two properties differ between the
// block and the query, and both matter:
//
//   - The BLOCK's length and alignment are guaranteed by the encoder, which
//     wrote it. f32sAt over the block stays.
//   - The QUERY is CALLER memory of caller-chosen length and alignment. Nothing
//     guarantees it is 4-aligned or a whole number of float32s, and a view
//     fabricates a length rather than deriving one, so it defeats the length
//     check veckernel.DotF32 performs on its arguments.
//
// A copy costs one allocation per SEARCH — not per distance, which is the term
// that would have mattered — and is what lets the width refusal below be the
// only guard the hot path needs.
type preparedQuery struct {
	// bytes is the query as given; the ubinary metric reads it directly.
	bytes []byte
	// f32 is the decoded query, non-nil only for a float32 segment. It is a
	// COPY, never a view over bytes.
	f32 []float32
}

// prepareQuery validates a query against this segment and converts it once.
//
// THE WIDTH CHECK IS HERE BECAUSE THIS IS THE ONE PLACE EVERY SEARCH PASSES
// THROUGH, so both dtypes get the same refusal from the same line. Before it,
// each arm failed differently and neither failed loudly: the float arm read past
// the caller's allocation, and the ubinary arm silently TRUNCATED to the shorter
// of the two buffers and ranked on a prefix — a wrong answer delivered as a
// successful one, which is the class this program refuses to ship.
//
// IT PANICS rather than returning an error, matching the package's existing
// register for a caller defect on the read path (idView, neighborsAt) and
// veckernel.DotF32's own length-mismatch panic, which this restores rather than
// replaces. searchengine.Segment.Search returns no error, so there is no channel
// to report it through; a wrong-width query reaching a segment is an engine
// routing defect, not a user input to be handled.
func (v *vectorBlock) prepareQuery(query []byte) preparedQuery {
	if len(query) != v.vecBytes {
		panic(fmt.Sprintf(
			"hnsw: query is %d bytes but this segment's vectors are %d bytes (%v)",
			len(query), v.vecBytes, ErrMixedVectorWidth))
	}
	if v.dtype != dtypeFloat32 {
		return preparedQuery{bytes: query}
	}
	dim := v.vecBytes / 4
	f := make([]float32, dim)
	for i := range dim {
		f[i] = math.Float32frombits(binary.LittleEndian.Uint32(query[i*4:]))
	}
	return preparedQuery{bytes: query, f32: f}
}

// distanceFn measures a prepared query against one stored vector. Lower is
// nearer, unconditionally: both heaps in traverse.go order by ascending
// distance, so a metric where bigger means better must invert (see
// distanceForDtype's float32 arm).
type distanceFn func(q *preparedQuery, vec []byte) float32

// batchScoreFn scores a whole run of node ids against a prepared query in one
// call, writing dst[i] for ids[i] in the same lower-is-nearer convention
// distanceFn uses.
//
// IT IS RESOLVED PER SEGMENT ALONGSIDE distance, by the same setDtype, for the
// same reason: a second seam that branched on dtype would be a second place the
// pair could drift apart. The batched neighbor scoring that consumes this calls
// one resolved function and never asks what dtype the segment is.
type batchScoreFn func(dst []float32, q *preparedQuery, vb *vectorBlock, ids []uint32)

// distanceForDtype picks the metric for a dtype. It is called ONCE PER SEGMENT,
// at open or at construction, never on the hot path — setDtype is the only
// caller and the traversal reads the resolved function value out of vectorBlock.
//
// THAT IS A MEASURED CONSTRAINT, NOT TIDINESS. The batching research measures
// a fixed, width-independent per-distance term of 5.6 ns on arm64 (38%
// of the dim-256 cost) and 8.8 ns on amd64 (50%). A per-call dtype branch would
// be charged against exactly the term that already dominates at narrow widths,
// so the selection is hoisted out of the loop entirely.
//
// THE FLOAT32 ARM RETURNS THE NEGATED DOT PRODUCT, and the sign inversion is
// written down here because a sign error in a comparator ranks backwards without
// ever looking like an error — no panic, no wrong length, just confidently
// reversed neighbors. Voyage-class embeddings are pre-normalized, so cosine
// similarity IS the dot product (veckernel exposes no separate cosine entry point
// for that reason) and dot is HIGHER-IS-BETTER. The candidate and result heaps
// order by ASCENDING distance, so the float arm stores -dot as the "distance";
// scoreForDtype undoes the negation to report the dot back as the score.
//
// ALIGNMENT of the float32 views: a mapped block starts 4-aligned by the v3
// emission order (see f32sAt), a built block is a Go byte allocation and so is
// at least 8-aligned, and every row sits at a whole multiple of vecBytes, which
// is a multiple of 4 for a float32 segment. Every row is therefore 4-aligned.
func distanceForDtype(dtype byte, vecBytes int) distanceFn {
	if dtype == dtypeFloat32 {
		dim := vecBytes / 4
		return func(q *preparedQuery, vec []byte) float32 {
			// q.f32 is a prepared COPY of the caller's query; only the BLOCK is
			// read through a typed view, and its length and alignment are the
			// encoder's guarantee. veckernel.DotF32's own length check is
			// therefore meaningful again — both slices have honest lengths.
			return -veckernel.DotF32(q.f32, f32sAt(vec, 0, dim))
		}
	}
	// hammingDistance stays in distance.go as the ubinary implementation. It is
	// reached only through this selection now, but it is not dead and must not be
	// deleted — it IS the tag-0 metric, which is what every segment written
	// before the dtype tag carries.
	return func(q *preparedQuery, vec []byte) float32 {
		return hammingDistance(q.bytes, vec)
	}
}

// nodeDistanceFn measures two STORED vectors against each other, in the same
// lower-is-nearer convention distanceFn uses.
//
// IT IS A SEPARATE SEAM FROM distanceFn BECAUSE THE OPERANDS DIFFER, not the
// metric. distanceFn takes a prepared query, which for float32 is a copy made
// because the caller's buffer carries no length or alignment guarantee. Both
// operands here are block vectors the encoder or the builder wrote: each is
// exactly vecBytes long and 4-aligned by construction, so both can be read
// through a typed view with no copy and no per-comparison allocation — which
// matters, because the build's hottest loop is a caller.
//
// WITHOUT THIS SEAM THE BUILD SILENTLY IGNORED THE DTYPE. Neighbor selection and
// the overflow re-prune called the Hamming distance directly, so a float32
// graph chose every neighbor list by comparing float bit patterns — a graph that
// serialized correctly, tagged correctly, and searched with the right metric
// over a topology built with the wrong one. Nothing fails; recall just degrades.
type nodeDistanceFn func(a, b []byte) float32

// nodeDistanceForDtype picks the stored-vs-stored metric for a dtype.
func nodeDistanceForDtype(dtype byte, vecBytes int) nodeDistanceFn {
	if dtype == dtypeFloat32 {
		dim := vecBytes / 4
		return func(a, b []byte) float32 {
			return -veckernel.DotF32(f32sAt(a, 0, dim), f32sAt(b, 0, dim))
		}
	}
	return hammingDistance
}

// batchScorerForDtype picks the batched scorer for a dtype, resolved once per
// segment beside the scalar metric.
//
// THE FLOAT32 ARM IS THE REASON THE FUSED KERNEL EXISTS: one call scores a whole
// neighbor run, so the query chunk stays in registers across candidate rows
// instead of being re-read per distance. The block enters as a typed view over
// encoder-written bytes and the ids are the run — exactly the ABI
// veckernel.DotF32Gather documents wanting, so wiring costs a view and no copy.
//
// Both arms write the SAME lower-is-nearer convention as distanceFn, so a caller
// can use the two interchangeably; the agreement between them is gated by test.
func batchScorerForDtype(dtype byte, vecBytes int) batchScoreFn {
	if dtype == dtypeFloat32 {
		dim := vecBytes / 4
		return func(dst []float32, q *preparedQuery, vb *vectorBlock, ids []uint32) {
			block := f32sAt(vb.vectors, 0, len(vb.vectors)/4)
			veckernel.DotF32Gather(dst, q.f32, block, dim, ids)
			// The kernel computes dot, which is higher-is-better; the heaps want
			// lower-is-nearer. Negating here keeps the inversion in the same one
			// place the scalar arm puts it, so scoreForDtype undoes exactly one
			// negation regardless of which path produced the distance.
			for i := range ids {
				dst[i] = -dst[i]
			}
		}
	}
	return func(dst []float32, q *preparedQuery, vb *vectorBlock, ids []uint32) {
		for i, id := range ids {
			dst[i] = hammingDistance(q.bytes, vb.nodeVector(id))
		}
	}
}

// setDtype records the block's dtype and resolves its metric together.
//
// THEY ARE SET AS A PAIR ON PURPOSE. Two separate assignments could drift — a
// caller that set the tag and forgot the metric would get a float32 segment
// silently ranked by Hamming distance over its own float bytes, which produces
// plausible-looking but wrong neighbors rather than any error. Making this the
// only way to set either field removes that state from the type.
//
// vecBytes must already be set: the float32 arms close over the derived dim.
//
// EVERY FIELD MOVES TOGETHER — the tag, the query metric, the stored-vs-stored
// metric and the batched scorer. Adding any of them as a separately-assigned
// field would reintroduce exactly the drift this function exists to remove, one
// level out: a segment whose search path ranked by dot while its BUILD path
// selected neighbors by Hamming would return the right metric over the wrong
// topology, and every one of those parts looks correct in isolation.
func (v *vectorBlock) setDtype(dtype byte) {
	v.dtype = dtype
	v.distance = distanceForDtype(dtype, v.vecBytes)
	v.nodeDistance = nodeDistanceForDtype(dtype, v.vecBytes)
	v.batchScore = batchScorerForDtype(dtype, v.vecBytes)
}

// scoreForDtype converts a traversal distance into the hit score the engine
// ranks on, per dtype. It is the SINGLE home of score normalization: duplicating
// it would let two paths drift while each one's tests still passed, and a
// criterion counts the ubinary formula's literal and requires exactly one
// occurrence in the package.
//
// UBINARY: the formula is moved verbatim from traverse.go, not rewritten — a
// Hamming distance over vecBytes*8 bits, expressed as a similarity in [0,1].
//
// FLOAT32: undoes the negation distanceForDtype applied, so the score is the raw
// dot product — which, for pre-normalized vectors, is the cosine similarity.
func scoreForDtype(dtype byte, dist float32, vecBytes int) float64 {
	if dtype == dtypeFloat32 {
		return float64(-dist)
	}
	totalBits := float64(vecBytes * 8)
	return 1.0 - float64(dist)/totalBits
}
