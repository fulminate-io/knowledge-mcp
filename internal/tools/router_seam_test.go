// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// Compile-time verification: *graphclient.Router must satisfy every type-
// assertion seam tools-package code performs on deps.GraphCaller(). If any
// of these fail to compile, the binary would break at runtime when
// manage/promote/persist/stats/sync paths call gc.(SeamInterface). Reviewer
// T1 regression guard for FUL-323.
//
// Seam → site map:
//   - Indexer             → intercept_manage_index.go:27 (Index RPC narrow)
//   - metadataStatsCaller → intercept_manage_promote.go:42 (MetadataStats RPC narrow)
//   - render.Executor     → cmd/knowledge/internal/projects/render/wire_fetch.go:31 (Execute RPC narrow)
//   - statsRPC            → intercept_query_cloud_cicd.go:236 (Stats + Execute narrow)
//   - Exporter            → intercept_sync.go:33 (ExportGraph RPC narrow); sync push
//     actually routes via LocalGraphCaller() in v1, but a Router that loses
//     ExportGraph would silently break any future caller that arrives via
//     the routed GraphCaller() path.
var (
	_ Indexer             = (*graphclient.Router)(nil)
	_ metadataStatsCaller = (*graphclient.Router)(nil)
	_ render.Executor     = (*graphclient.Router)(nil)
	_ statsRPC            = (*graphclient.Router)(nil)
	_ Exporter            = (*graphclient.Router)(nil)
)
