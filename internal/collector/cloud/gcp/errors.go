// SPDX-License-Identifier: Apache-2.0

// Package gcp — shared error-classification helpers for GCP subcollectors.
//
// GCP SDK surfaces mix two error transports: REST-based clients (e.g. the
// compute v1 API) return *googleapi.Error with an HTTP Code, while gRPC-based
// clients (e.g. Cloud Identity, Artifact Registry) return errors carrying a
// google.golang.org/grpc/codes status. Both can surface permission-denied on
// the same collector run — so subcollectors must recognize both.
package gcp

import (
	"errors"
	"net/http"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isPermissionDenied reports whether err represents a permission-denied
// response from either a REST (googleapi.Error, HTTP 403) or gRPC
// (codes.PermissionDenied) GCP client.
//
// The two cases collapse to the same caller semantics: the credential lacks
// access to the requested scope and the caller should either skip that scope
// (per-zone aggregated list) or return an empty result with nil error
// (project-level deny).
func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusForbidden {
		return true
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.PermissionDenied {
		return true
	}
	return false
}

// isZoneUnreachableWarning reports whether the warning attached to an
// aggregated-list scope entry indicates the scope was unreachable for this
// caller — typically because the credential lacks permission for that zone or
// the zone is administratively restricted (e.g. GKE Autopilot managed nodes).
//
// This is the signal Google recommends checking when callers set
// returnPartialSuccess=true on aggregated-list requests; it also appears
// opportunistically on default (non-partial-success) responses.
func isZoneUnreachableWarning(w *computepb.Warning) bool {
	if w == nil {
		return false
	}
	return w.GetCode() == computepb.Warning_UNREACHABLE.String()
}

// aggregatedIterDecision is the verdict for a single it.Next() error from a
// compute AggregatedList iterator, after classifying it against how many
// pairs the loop has already yielded.
type aggregatedIterDecision int

const (
	// aggregatedIterAbort — the error is not permission-denied; propagate it.
	aggregatedIterAbort aggregatedIterDecision = iota
	// aggregatedIterEmpty — project-level permission-denied on the very first
	// Next() call (no pairs yielded). Caller should return the empty result
	// with nil error so the parent collector can continue with other
	// subcollectors instead of failing the whole run.
	aggregatedIterEmpty
	// aggregatedIterSkipZone — mid-iteration permission-denied for one scope.
	// In practice the iterator surfaces this once and then transitions to Done
	// on the next call, but callers should be defensive: treat as "stop this
	// loop, return what we've accumulated with nil error".
	aggregatedIterSkipZone
)

// classifyAggregatedIterErr decides how a compute AggregatedList iterator
// error should be handled. yielded is the number of pair results successfully
// returned from the iterator before this error.
//
// Non-permission errors always abort. Permission-denied errors with no pairs
// yielded yet mean the project itself is inaccessible (return empty+nil).
// Permission-denied errors after at least one pair yielded mean a specific
// zone is restricted (skip and keep what we have).
func classifyAggregatedIterErr(err error, yielded int) aggregatedIterDecision {
	if !isPermissionDenied(err) {
		return aggregatedIterAbort
	}
	if yielded == 0 {
		return aggregatedIterEmpty
	}
	return aggregatedIterSkipZone
}
