// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// storageRebuildInputs binds both the single-flight key and the segment owner.
func storageRebuildInputs(ctx context.Context, gt kgtypes.GraphType, name string, shipper SegmentShipper) (string, SegmentShipper) {
	key := string(gt) + "/" + name
	if destination, bound := graphclient.StorageDestination(ctx); bound {
		key = fmt.Sprintf("%q/%q/%s", destination.Storage, destination.AccountID, key)
	}
	if owner, ok := shipper.(interface {
		ForStorage(context.Context) SegmentShipper
	}); ok {
		shipper = owner.ForStorage(ctx)
	}
	return key, shipper
}
