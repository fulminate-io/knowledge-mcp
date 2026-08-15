// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// unpageablePayloadError reports a browse payload drainQueryNodes refuses to
// drain because it does not reach the singular type-browse arm — the only arm
// the id-keyset cursor rides. Key names the key responsible.
type unpageablePayloadError struct {
	Key    string
	Reason string
}

func (e *unpageablePayloadError) Error() string {
	return fmt.Sprintf("tools: browse payload key %q cannot be drained in keyset pages: %s", e.Key, e.Reason)
}

// precedenceKeysAboveType are the query keys the compiler dispatches on BEFORE
// the singular type browse, in its own precedence order.
var precedenceKeysAboveType = []string{"ids", "id", "text", "types"}

// requireSingularTypeBrowse refuses, before any RPC, a payload the keyset drain
// cannot page. It checks FOUR keys rather than one because the compiler
// dispatches a mode-less query in strict precedence order — ids, id, text,
// types, type, meta — and only the singular type-browse arm threads after_id. A
// payload carrying "type" PLUS any higher-precedence key takes the
// higher-precedence arm, which ignores the cursor: every page would return the
// same first page, the drain would never see a short page, and the loop would
// not terminate. Refusing loudly beats hanging.
func requireSingularTypeBrowse(args map[string]any) error {
	for _, k := range precedenceKeysAboveType {
		if _, ok := args[k]; ok {
			return &unpageablePayloadError{
				Key:    k,
				Reason: "it outranks the singular type browse, and its arm threads no keyset cursor",
			}
		}
	}
	t, ok := args["type"].(string)
	if !ok || strings.TrimSpace(t) == "" {
		return &unpageablePayloadError{
			Key:    "type",
			Reason: "a non-blank singular type is what selects the arm the keyset cursor rides",
		}
	}
	return nil
}

// drainQueryNodes returns EVERY node matching a type-browse payload, read as
// bounded id-keyset pages, where a single executeQuery performs one bounded
// read. It is the helper for a tools caller that intends "all".
//
// A limit of 0 is a bounded DEFAULT at this seam, never a request for
// everything: the client compiler rewrites a non-positive limit to the browse
// default, and the server clamps any limit to its own row ceiling. Both bounds
// are deliberate and unchanged — a shared system never serves an unbounded
// query, it pages — so "all" is spelled as a drain rather than as a large limit.
//
// Cross-page ordering is id-ASCENDING and stable on both backends, which is what
// makes the cursor total: each page asks for the ids strictly greater than the
// last id of the page before it, and page one passes a SET BUT EMPTY cursor
// (presence is what selects the keyset browse; an omitted cursor would page in
// the backend's own default order and skip every lower id).
//
// It delegates per page to the existing executeQuery, so the compile + Execute
// seam and its error propagation stay in one place. drainNodesOfType is
// deliberately NOT the reuse target here: it is plan-based rather than
// args-based and it SWALLOWS its error, returning nil on failure, while every
// caller of this helper propagates.
func drainQueryNodes(ctx context.Context, gc GraphCaller, args map[string]any) ([]*knowledgev1.Node, error) {
	if err := requireSingularTypeBrowse(args); err != nil {
		return nil, err
	}
	return paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		page := make(map[string]any, len(args)+3)
		maps.Copy(page, args)
		// Set AFTER the copy: writing the page keys last is what stops a
		// caller's stale limit from defeating the drain.
		page["limit"] = paging.BrowsePageSize
		page["after_id"] = afterID
		page["skip_total"] = true
		body, err := json.Marshal(page)
		if err != nil {
			return nil, fmt.Errorf("tools: marshal browse page: %w", err)
		}
		resp, err := executeQuery(ctx, gc, body)
		if err != nil {
			return nil, err
		}
		return engine.DecodeNodes(resp)
	}, paging.BrowsePageSize)
}
