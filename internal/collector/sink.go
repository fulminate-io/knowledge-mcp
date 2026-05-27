// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// Sink is the terminal destination every write path in the collector package
// routes through. WriteResult performs a full-replace or overlay write of a
// CollectResult, including cross-graph linker for cloud/CICD graphs. Used by
// pipeline.Collect.
//
// Implementations that pull in the indexer/write-path live in subpackages
// (collector/local for the in-process store-singleton sink, collector/remote
// for the connect-go upload sink) so the collector root package stays free
// of store write-path transitive imports.
type Sink interface {
	WriteResult(ctx context.Context, collectorName string, result *collectorwire.CollectResult) error
}
