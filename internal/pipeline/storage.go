// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// bind restores a queued work item's destination for downstream cache writes.
func (k graphKey) bind(ctx context.Context) context.Context {
	if k.Destination.Storage == "" {
		return ctx
	}
	return graphclient.WithDestination(ctx, k.Destination)
}
