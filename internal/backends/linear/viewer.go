// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"context"
	"errors"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// viewerQuery asks Linear who the key belongs to. It is the cheapest
// authenticated read the API documents — no pagination, no rate-limit
// complexity class, and it fails with 401 for a key Linear does not
// recognize, which is exactly the answer a credential check wants.
//
// It is a SEPARATE query from the operation constants in queries_read.go
// and queries_write.go, because none of them is a credential probe: every
// one of them reads or writes issue data, so using one to test a key would
// report a missing team or an empty project as an authentication failure.
const viewerQuery = `query Me { viewer { id } }`

// ErrViewerUnidentified reports a 200 response that named no viewer. Linear
// answers the viewer query with an id for every valid personal API key, so
// an empty one is an endpoint that is not Linear rather than a key that
// works — a proxy, a captive portal, or a stub.
var ErrViewerUnidentified = errors.New("linear: the endpoint answered the viewer query without naming a viewer")

// CheckViewer reports whether c's key is one Linear accepts, by issuing the
// viewer query against c's endpoint.
//
// Enabled() is NOT this: it only tests that the key string is non-empty
// (client.go:53), which is why a wrong key reaches the first real
// operation before anyone finds out. This makes the call.
//
// Returns nil when Linear identified a viewer. Otherwise the error is the
// transport's own classified *backends.Error — ReasonAuth for 401/403,
// ReasonRateLimited for 429 — so a caller maps the outcome without
// re-deriving it from an HTTP response, or ErrViewerUnidentified.
func (c *Client) CheckViewer(ctx context.Context) error {
	var out struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}
	if err := c.do(ctx, viewerQuery, nil, &out); err != nil {
		return err
	}
	if out.Viewer.ID == "" {
		return &backends.Error{
			Transient: false,
			Reason:    backends.ReasonGraphQL,
			Cause:     fmt.Errorf("%w: %s", ErrViewerUnidentified, c.Endpoint),
		}
	}
	return nil
}
