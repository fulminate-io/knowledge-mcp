// SPDX-License-Identifier: Apache-2.0

// Package segmentdist is the CLIENT-side segment distribution layer: a
// content-addressed on-disk L2 SegmentCache, a GCS-agent SegmentSource for the
// logged-in cloud path, an L2-only local SegmentSource for the OSS not-logged-in
// path, and a load/unload manager that ties the searchengine engine to whichever
// source the graph runs on. It is a CONSUMER of
// cmd/knowledge/internal/searchengine — deliberately a SIBLING package, NOT inside
// the engine subpackage, so the engine stays import-clean (stdlib + own subpkgs)
// for a future service extraction (locked contract).
package segmentdist

import (
	"context"
	"errors"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// errNoSegmentTransportBuilder / errNilSegmentTransport are the reasons the source
// factory hands the errorSegmentSource sentinel when a logged-in cloud caller has
// no usable segment transport: no builder was supplied, or a supplied builder
// returned a nil transport. A builder that returns a non-nil error carries that
// error through as the reason instead.
var (
	errNoSegmentTransportBuilder = errors.New("no segment transport builder supplied to a logged-in cloud Manager")
	errNilSegmentTransport       = errors.New("segment transport builder returned a nil transport")
)

// loginState is the cloud-capability signal the source factory gates on: whether
// the caller (production *graphclient.Router) is in a logged-in cloud session.
// Declared here (consumer-side interface) so the package has no hard dependency on
// a concrete client type; *graphclient.Router satisfies it via its LoggedIn method.
type loginState interface {
	LoggedIn(ctx context.Context) bool
}

// segmentSource is the pluggable segment-distribution seam distManager depends on.
// It SUPERSETS searchengine.SegmentSource (List/Fetch) with the ship/prune/publish
// legs so a cloud (GCS-agent) or OSS (local-L2) source plugs into the manager
// through one interface. Ship is proto-typed on the shared wire contract
// (SegmentBlobProto/SegmentMetaProto) — every source speaks it.
type segmentSource interface {
	searchengine.SegmentSource // List(ctx, sinceGen) []SegmentMeta; Fetch(ctx, ids) []SegmentBlob
	Ship(ctx context.Context, blobs []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error)
	Prune(ids []searchengine.SegmentID) (int, error)
	PublishManifest(format string, digests []segmentDigest) (int, error)
	// verifiesCompletenessServerSide reports whether the source's PublishManifest
	// verifies live-set completeness on the server/agent side (a HEAD-verify + a
	// 409-on-missing), making the client's liveSetSubsetOfList0 publish check
	// redundant AND wrong. It is true ONLY on the GCS source, where List(0) IS the
	// published manifest — so a resident set that legitimately includes
	// newly-shipped-but-not-yet-published blobs is never a subset of List(0) and would
	// deadlock the first/every add-publish. The local source returns false (its
	// List(0) is the L2 set, against which the subset check is the correct
	// incomplete-view guard).
	verifiesCompletenessServerSide() bool
}

// segmentDigest is one blob's identity in a manifest publish: its content-hash id
// plus its live doc count. Carrying doc_count through PublishManifest lets the GCS
// manifest store a real per-digest denominator for the coverage reads.
type segmentDigest struct {
	ID       searchengine.SegmentID
	DocCount int
}

// errorSegmentSource is the FAIL-LOUD sentinel the source factory returns when a
// logged-in cloud caller's segment transport cannot be built (nil/failed builder).
// Every mutating/reading leg returns a wrapped, operator-actionable error rather
// than silently degrading — a logged-in client with a broken transport must NOT
// fall back to a phantom local source or a deleted RPC path; it must surface the
// misconfiguration. verifiesCompletenessServerSide returns false (a bool, not an
// error): the publish subset-check guard must have a definite answer, and false is
// the safe/inert value (it never suppresses the incomplete-view guard).
//
// The source is memoized per graph, so a transport-build failure PINS this sentinel
// for that graph until the daemon restarts — accepted: a logged-in client whose
// transport build fails is misconfigured, and a restart after fixing the config
// re-selects the GCS source.
type errorSegmentSource struct {
	reason error
}

var _ segmentSource = (*errorSegmentSource)(nil)

// segmentTransportErr wraps the underlying transport-build failure into an
// operator-actionable message for every errorSegmentSource leg.
func (s *errorSegmentSource) segmentTransportErr(op string) error {
	return fmt.Errorf("segmentdist: %s unavailable — logged-in cloud segment transport failed to build (%v); check cloud credentials/connectivity and restart the daemon", op, s.reason)
}

func (s *errorSegmentSource) List(context.Context, uint64) ([]searchengine.SegmentMeta, error) {
	return nil, s.segmentTransportErr("segment List")
}

func (s *errorSegmentSource) Fetch(context.Context, []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	return nil, s.segmentTransportErr("segment Fetch")
}

func (s *errorSegmentSource) Ship(context.Context, []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	return nil, s.segmentTransportErr("segment Ship")
}

func (s *errorSegmentSource) Prune([]searchengine.SegmentID) (int, error) {
	return 0, s.segmentTransportErr("segment Prune")
}

func (s *errorSegmentSource) PublishManifest(string, []segmentDigest) (int, error) {
	return 0, s.segmentTransportErr("segment PublishManifest")
}

// verifiesCompletenessServerSide returns false: the sentinel has no server-side
// completeness verification, so the client's incomplete-view subset guard stays
// armed. It is a bool (not an error) so the publish gate always has a definite
// answer — the T4-1 interface-shape requirement.
func (s *errorSegmentSource) verifiesCompletenessServerSide() bool { return false }

// blobToProto maps an engine SegmentBlob to the wire carrier. Used by the
// manager's ship path.
func blobToProto(b searchengine.SegmentBlob) *knowledgev1.SegmentBlobProto {
	return &knowledgev1.SegmentBlobProto{
		Id:         b.ID,
		Format:     b.Format,
		Generation: b.Generation,
		DocCount:   int32(b.DocCount),
		Bytes:      b.Bytes,
	}
}

// blobFromProto maps a wire carrier to an engine SegmentBlob.
func blobFromProto(p *knowledgev1.SegmentBlobProto) searchengine.SegmentBlob {
	return searchengine.SegmentBlob{
		ID:         p.GetId(),
		Format:     p.GetFormat(),
		Generation: p.GetGeneration(),
		DocCount:   int(p.GetDocCount()),
		Bytes:      p.GetBytes(),
	}
}
