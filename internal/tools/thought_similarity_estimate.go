// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// similarityDefaultEstimate is the coarse honest range rendered when no prior
// completed pass exists to calibrate against.
const similarityDefaultEstimate = "likely several minutes to ~20 minutes — no prior pass to calibrate against"

// similarityEstimate renders the duration estimate for the trigger/coalesce
// response AND the in-progress fetch — one helper so the estimate text is identical
// across all three sites. It reads the latest COMPLETED pass through the forcer, so the
// status=running event the trigger just created no longer blinds it (the Defect-2 fix:
// reading the latest ANY-status event always found that fresh running record, which has
// no duration_ms, and fell back to the default forever). When a completed pass with a
// parseable, positive duration_ms exists it phrases the estimate off that real duration;
// otherwise it returns the coarse default range. Reads through the forcer (the
// thought-side event seam), never deps.GraphCaller().
func similarityEstimate(forcer SimilarityForcer, ctx context.Context) string {
	n, ok := forcer.LatestCompletedSimilarityEvent(ctx)
	if !ok || n == nil {
		return similarityDefaultEstimate
	}
	ms, err := strconv.ParseInt(kgtypes.Value(n, clientthought.MetaSimDurationMs), 10, 64)
	if err != nil || ms <= 0 {
		return similarityDefaultEstimate
	}
	d := time.Duration(ms) * time.Millisecond
	return fmt.Sprintf("~%s based on the last completed pass", d.Round(time.Second))
}
