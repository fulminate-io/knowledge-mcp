// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// ProgressFunc reports collection progress to callers.
type ProgressFunc func(current, total int, message string)

// CollectOptions configures a collection run.
type CollectOptions struct {
	OnProgress ProgressFunc
	Force      bool // skip safety check for existing indexed graphs
	Sink       Sink // terminal destination; nil = buildSink(DefaultSinkConfig())
}

// Collector is the interface that all collectors implement.
// Each collector turns an external source into graph nodes and edges.
type Collector interface {
	Name() string
	Collect(ctx context.Context, id string, opts CollectOptions) (*collectorwire.CollectResult, error)
}
