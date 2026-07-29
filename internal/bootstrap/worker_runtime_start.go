// SPDX-License-Identifier: Apache-2.0

// worker_runtime_start.go — the query-origin stamp on the worker runtime's
// boot-time registry load. Split out of dream.go, which is at the file-length
// cap.

package bootstrap

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// workerRuntimeStarter is the narrow Start half of the dream Runner, taken as an
// interface so the boot stamp is assertable without standing up a whole client.
type workerRuntimeStarter interface {
	Start(ctx context.Context) error
}

// startWorkerRuntime runs the boot-time Runner.Start under a query-origin
// stamp. Start loads the worker registry over the wire (Runner.Start →
// Registry.All → the workercrud lister), and that read has no originating tool
// call, so without the stamp it arrives as client.unstamped — one anonymous
// query per daemon boot.
func startWorkerRuntime(ctx context.Context, runner workerRuntimeStarter) error {
	return runner.Start(graphclient.WithOperation(ctx, graphclient.OpWorkerRuntimeStart))
}
