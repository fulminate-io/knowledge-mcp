// SPDX-License-Identifier: Apache-2.0

//go:build collectbench

package remote

// sink_benchfull.go — the bench's NON-DIFF arm, and nothing else.
//
// WHY IT IS BEHIND A BUILD TAG. The convergence run needs one arm that never
// computes a diff, so that "diff-landed equals full-landed" is a comparison
// between two different code paths rather than a determinism check on one. That
// arm must not be reachable in a shipped binary, and a tag removes the symbol
// from the build entirely rather than leaving a lever an operator could find.
//
// IT IS NOT A HOLE THE LEVER TABLE LEAVES OPEN. The `off` and `shadow` lever
// values stamp identical wire (both send diff_mode=false); shadow merely costs
// more, because it renders a manifest and then uploads everything anyway. So
// this constructor closes nothing a user could otherwise do — the protection
// against a client that uploads everything is SERVER-SIDE O(diff) enforcement,
// not the absence of this symbol. The tag is chosen for the narrow and honest
// reason above.

// NewUploadSinkForBenchFullPath constructs an UploadSink that uploads EVERY file
// on every collect: no CollectManifest fetch, no diff computed, exactly the
// pre-incremental client's behavior.
//
// It is the third constructor over the same struct, following NewUploadSink and
// NewUploadSinkFunc — the established idiom in this package for a sink variant
// is a constructor setting different fields, not a second type.
func NewUploadSinkForBenchFullPath(picker IngestClientPicker) *UploadSink {
	return &UploadSink{picker: picker, benchForceFullNoDiff: true}
}
