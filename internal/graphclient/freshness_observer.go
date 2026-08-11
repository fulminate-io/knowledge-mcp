// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"sync/atomic"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// freshnessGenField is the response field the observer resolves BY NAME on each
// response descriptor. Resolving by name rather than by a per-message type
// switch is the same argument the request-side stamper records about itself
// (operation_interceptor.go:15-20): a type switch would have to be extended by
// hand every time a response message is added, and the one that got missed would
// ship reading no watermark at all. A descriptor lookup covers every covered
// message that exists today and every one added later, for free.
//
// The WRITE half of this same field lives in the server module's response
// stamper. That code is in the other module, so the client cannot call it —
// this is its read-side mirror, not a shared helper.
const freshnessGenField protoreflect.Name = "freshness_gen"

// newFreshnessObserver returns the interceptor that records the account
// freshness watermark carried on every successful response into sink.
//
// The wire contract, quoted from proto/knowledge/v1/engine.proto:
//
//	"account-scoped freshness watermark: a monotonic counter the server advances
//	whenever ANYTHING in this account changed"
//	"THE SERVED VALUE IS A PER-REPLICA SAMPLE of that counter, not the counter
//	itself: each replica caches it on a short TTL and refreshes asynchronously,
//	so the value a client observes MAY MOVE BACKWARD between replicas or after a
//	restart."
//	"A client must therefore treat ANY CHANGE as movement and never test only
//	for an increase."
//	"It is a NOTIFICATION, never state: 0 means the serving flavor maintains no
//	watermark ... or this replica has not loaded one yet."
//
// A zero is therefore NOT recorded. It is the value a server serves before its
// first bump, or one from a flavor that maintains none, so storing it would make
// a cold process's first response look like movement away from a real value.
//
// Nothing here may alter a response: a handler error is returned untouched, and
// a message that does not declare the field is a silent no-op.
//
// It costs one descriptor lookup and one atomic store per response — no
// allocation, no I/O. It runs inline on the response path, so serial is the only
// option; there is nothing here to parallelize.
func newFreshnessObserver(sink *atomic.Uint64) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if err != nil {
				// No response to read, and an observer must never alter an error.
				return resp, err
			}
			observeFreshnessGen(resp.Any(), sink)
			return resp, nil
		}
	})
}

// observeFreshnessGen stores msg's freshness_gen into sink when the message
// declares it as a uint64 and the value is non-zero. It is split out of the
// interceptor closure so tests can drive it directly, mirroring stampOperation.
//
// The nil/kind guard mirrors the server's setUint64Field: an absent or
// differently-typed field must be a silent no-op, never a protoreflect panic on
// a live response path.
func observeFreshnessGen(msg any, sink *atomic.Uint64) {
	pm, ok := msg.(proto.Message)
	if !ok {
		return
	}
	refl := pm.ProtoReflect()
	fd := refl.Descriptor().Fields().ByName(freshnessGenField)
	if fd == nil || fd.Kind() != protoreflect.Uint64Kind {
		return
	}
	if v := refl.Get(fd).Uint(); v != 0 {
		sink.Store(v)
	}
}
