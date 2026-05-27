// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_dispatch.go implements the Phase 9.5 cross-account
// rule-dispatch orchestrator. Extracted from iam_escalation.go to keep
// that file under the 300-line soft cap after Phase 9.5 added
// cross-account walking. This file owns three responsibilities:
//
//  1. dispatchAcrossAccounts — run dispatchIAMRules for every loaded
//     cloud graph and merge the results into a single inferred-edge
//     map, a single admin set, and a per-account scoped-reader lookup.
//  2. mergeInferred / mergeAdmins — small set-merge helpers factored
//     out so dispatchAcrossAccounts stays readable.
//
// The BFS in iam_escalation_paths.go consumes the merged maps exactly
// as the single-account BFS consumed dispatchIAMRules's output, so the
// walker is unchanged apart from the visitKey tuple extension.

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// dispatchAcrossAccounts runs dispatchIAMRules for every loaded cloud
// graph (the analyzer's primary account + every other account returned
// by FetchGraphNames). Per-account inferred edges are merged into a single
// map keyed by FromID (ARNs are globally unique) and per-account admin
// sets are merged into one boolean map. Cross-account walks (Phase 9.5,
// OQ-7) rely on this pre-computation so the BFS can traverse into any
// account's edge neighborhood without querying rule-level state at walk
// time.
//
// The returned scopedByAccount map lets the BFS resolve the correct
// per-account scoped reader when it needs to query native EdgeAssumesRole
// edges after pivoting into a new account context.
//
// The primary rctx (for req.Name) is reused so we don't double-scan its
// principals. Other accounts each get their own fresh iamRuleContext.
func dispatchAcrossAccounts(
	ctx context.Context,
	caller foundation.GraphCaller,
	primary *iamRuleContext,
) (
	inferred map[string][]iamInferredEdge,
	admins map[string]bool,
	scopedByAccount map[string]*cloudReader,
	err error,
) {
	inferred = make(map[string][]iamInferredEdge)
	admins = make(map[string]bool)
	scopedByAccount = map[string]*cloudReader{primary.Account: primary.scoped}

	// Primary account first.
	pInf, pAdm, err := dispatchIAMRules(ctx, primary)
	if err != nil {
		return nil, nil, nil, err
	}
	mergeInferred(inferred, pInf)
	mergeAdmins(admins, pAdm)

	// Other accounts: enumerate and dispatch. A failure enumerating the
	// loaded cloud graphs is best-effort — the legacy in-memory
	// ListGraphsLite read could not fail, so a wire hiccup here degrades to
	// "primary account only" rather than aborting the whole analyzer (which
	// would discard the primary-account findings already computed above).
	infos, lerr := foundation.FetchGraphNames(ctx, caller, kgtypes.GraphCloud)
	if lerr != nil {
		// Intentional swallow: the legacy ListGraphsLite read was infallible,
		// so a wire-side enumeration failure degrades to "primary account
		// only" and returns the already-computed primary findings rather than
		// discarding them. Returning the error would abort the whole analyzer.
		return inferred, admins, scopedByAccount, nil //nolint:nilerr // best-effort cross-account enumeration; primary findings preserved
	}
	for _, gi := range infos {
		if gi.Name == "" || gi.Name == primary.Account {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, fmt.Errorf("topology/iam_escalation: %w", err)
		}
		other := newCloudReader(caller, gi.Name)
		scopedByAccount[gi.Name] = other
		rctx, berr := buildIAMRuleContext(ctx, caller, other, gi.Name)
		if berr != nil {
			continue
		}
		if len(rctx.Roles)+len(rctx.Users)+len(rctx.Groups) == 0 {
			continue
		}
		oInf, oAdm, derr := dispatchIAMRules(ctx, rctx)
		if derr != nil {
			return nil, nil, nil, derr
		}
		mergeInferred(inferred, oInf)
		mergeAdmins(admins, oAdm)
	}
	return inferred, admins, scopedByAccount, nil
}

// mergeInferred folds src into dst keyed by FromID. ARN keys are
// globally unique so same-key collisions only happen when two rule
// dispatches (same account re-dispatched, or one principal visible in
// two accounts) emit edges from the same principal. Both cases are
// handled by appending — downstream dedup collapses duplicates by
// (source, target) tuple.
func mergeInferred(dst, src map[string][]iamInferredEdge) {
	for k, v := range src {
		dst[k] = append(dst[k], v...)
	}
}

// mergeAdmins folds src into dst. Any principal flagged admin in any
// loaded account stays admin in the combined view.
func mergeAdmins(dst, src map[string]bool) {
	for k, v := range src {
		if v {
			dst[k] = true
		}
	}
}
