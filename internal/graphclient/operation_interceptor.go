// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// clientContextField / operationField are the proto field names the stamping
// interceptor resolves BY NAME on each outbound request descriptor. Resolving
// by name is the whole design: a per-message type switch would have to be
// extended by hand every time a request message is added, and the one that got
// missed would ship issuing unlabeled RPCs. A descriptor lookup covers every
// covered message that exists today and every one added later, for free.
const (
	clientContextField protoreflect.Name = "client_context"
	operationField     protoreflect.Name = "operation"
)

// OpUnstamped is the reserved term stamped when a request whose message CARRIES
// the client_context field reaches the interceptor with no operation in ctx. It
// is shape-valid, so the server accepts the RPC rather than failing a user's
// call over an instrumentation defect — but it means exactly one thing: a client
// stamping bug. It is rendered as its own bucket in the per-tag tooling,
// deliberately distinct from an unswept builder and from legitimate server-side
// background work, so a stamping bug surfaces instead of hiding inside normal
// traffic.
const OpUnstamped Operation = "client.unstamped"

// inTestBuild discriminates default-deny's two halves. It is a var rather than a
// direct testing.Testing() call for one reason: under `go test` the production
// half would otherwise be unreachable, so the branch that actually ships could
// never be exercised. Tests swap it to drive that branch.
var inTestBuild = testing.Testing

// newOperationInterceptor returns the single outbound interceptor that stamps
// the client operation onto every covered request. There is exactly ONE of
// these, installed on every constructed client, because "which RPCs get
// labeled" must not be a per-call-site decision.
//
// Coverage is decided by the MESSAGE, not by a service list: a request whose
// descriptor has no client_context field (HealthService's, deliberately, so
// credential-less liveness probes from orchestrators we do not control keep
// working) passes through untouched.
//
// Default-deny has two halves and both are live here. In a TEST build an
// unstamped covered request fails loudly, so the defect is caught before it
// ships. In a production build the same condition stamps OpUnstamped, so a
// defect that shipped anyway is visible in the metrics instead of silent.
func newOperationInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if err := stampOperation(ctx, req.Any(), req.Spec().Procedure); err != nil {
				return nil, err
			}
			return next(ctx, req)
		}
	})
}

// stampOperation writes the ctx operation into msg's client_context.operation,
// reporting an error only for the test-build default-deny case. It is split out
// of the interceptor closure so tests can drive it directly, and returns early
// (no-op) for anything that is not a covered proto message.
func stampOperation(ctx context.Context, msg any, procedure string) error {
	pm, ok := msg.(proto.Message)
	if !ok {
		return nil
	}
	refl := pm.ProtoReflect()
	fd := refl.Descriptor().Fields().ByName(clientContextField)
	if fd == nil || fd.Kind() != protoreflect.MessageKind {
		// Not a covered request (HealthService). Nothing to stamp, and NOT a
		// default-deny violation — an uncovered message is uncovered by design.
		return nil
	}
	opFd := fd.Message().Fields().ByName(operationField)
	if opFd == nil || opFd.Kind() != protoreflect.StringKind {
		return nil
	}

	// An operation already set on the message wins: a caller that stamped
	// explicitly is more specific than whatever the ambient ctx carries.
	if refl.Has(fd) && refl.Get(fd).Message().Get(opFd).String() != "" {
		return nil
	}

	op, ok := OperationFromContext(ctx)
	if !ok {
		if inTestBuild() {
			return connect.NewError(connect.CodeInternal, fmt.Errorf(
				"graphclient: %s is a covered RPC issued with no operation in context — "+
					"wrap the call site's ctx with graphclient.WithOperation(ctx, <Operation>)",
				procedure))
		}
		op = OpUnstamped
	}
	refl.Mutable(fd).Message().Set(opFd, protoreflect.ValueOfString(string(op)))
	return nil
}
