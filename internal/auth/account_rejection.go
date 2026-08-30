// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// The gateway's rejection vocabulary, frozen with the companion gateway
// ticket. These are the slugs observable on this client's cloud traffic —
// /v1/sync/* and the Connect-forwarded /knowledge.v1.* calls.
//
// On those routes the gateway's tier gate owns account resolution and answers
// first, so the `no_account` and `account_lookup_failed` slugs are NEVER
// observable here: both conditions arrive as a 403 subscription_required body
// discriminated only by error_description. Do not branch on those slugs for
// these routes — such code is dead in every state of the system. They ARE
// observable on the bearer-gated GET /v1/me/accounts list endpoint, which is
// not tier-gated.
const (
	errSlugAccountForbidden     = "account_forbidden"
	errSlugBadRequest           = "bad_request"
	errSlugSubscriptionRequired = "subscription_required"
)

// The five error_description values that ride a single 403
// subscription_required. Only the first two are settled answers about the
// caller's entitlement; the rest are server-side or wiring failures.
const (
	descSubscriptionRequired = "active paid subscription required"
	descNoAccount            = "no account associated with this user"
	descAccountLookupFailed  = "account lookup failed"
	descSubLookupFailed      = "subscription lookup failed"
	descMissingAuthContext   = "missing authentication context"
)

// gatewayErrorBody is the gateway's rejection JSON. Extra fields are ignored.
type gatewayErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	UpgradeURL       string `json:"upgrade_url"`
}

// ClassifyAccountRejection maps a gateway rejection onto an operator-facing
// reason and a decision about whether the SELECTION is now known-invalid.
//
// reason is empty when the response is not an account rejection at all. latch
// reports that the stored selection is settled-bad: mark it invalid and fail
// fast locally until the user selects a different account.
//
// The decision table:
//
//   - 403 account_forbidden                      -> latch, membership remedy
//   - 400 bad_request                            -> latch, malformed-id remedy
//   - 403 subscription_required with description
//     "active paid subscription required" or
//     "no account associated with this user"     -> latch, subscription remedy
//   - 403 subscription_required with description
//     "account lookup failed", "subscription
//     lookup failed" or "missing authentication
//     context"                                   -> NO latch, gateway's own reason
//   - anything else, including a 403 whose body
//     will not parse                             -> not a rejection
//
// WHY THE TWO lookup-failed DESCRIPTIONS MUST NOT LATCH: they are transient
// SERVER-SIDE failures — a membership or subscription read that erred, not an
// answer about the caller's entitlement. Latching on them would fail the user
// out of a perfectly valid account for the life of the process (the in-memory
// marker only self-clears when the stored id changes), so one gateway database
// hiccup would look like a revoked account. "missing authentication context"
// is a gateway wiring error and is not about the account at all. Same
// conservatism as the parse-failure rule: a client that fails itself out of a
// working account on an ambiguous or transient response is worse than one that
// makes another round trip.
//
// A 401 is never an account rejection — it is a bearer problem, already
// handled by the force-refresh-and-retry-once paths.
func ClassifyAccountRejection(status int, body []byte) (reason string, latch bool) {
	if status == http.StatusUnauthorized || status < 400 {
		return "", false
	}
	var parsed gatewayErrorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false
	}

	switch parsed.Error {
	case errSlugAccountForbidden:
		if status != http.StatusForbidden {
			return "", false
		}
		return "you are not a member of the selected Fulminate account — run `knowledge accounts` to see the accounts you can use, then `knowledge account use <id|slug>`", true

	case errSlugBadRequest:
		if status != http.StatusBadRequest {
			return "", false
		}
		return "the stored Fulminate account id is malformed — run `knowledge account use <id|slug>` to set a valid one", true

	case errSlugSubscriptionRequired:
		if status != http.StatusForbidden {
			return "", false
		}
		switch parsed.ErrorDescription {
		case descSubscriptionRequired, descNoAccount:
			where := parsed.UpgradeURL
			if where == "" {
				where = "billing"
			}
			return fmt.Sprintf("the selected Fulminate account has no active subscription, so it has no cloud graph access — subscribe at %s, or run `knowledge account use <id|slug>` to pick an account that does", where), true
		case descAccountLookupFailed, descSubLookupFailed, descMissingAuthContext:
			return fmt.Sprintf("the Fulminate gateway could not complete the account check: %s", parsed.ErrorDescription), false
		default:
			// An unrecognized description on a known slug: surface it without
			// settling the selection's validity.
			if parsed.ErrorDescription == "" {
				return "", false
			}
			return fmt.Sprintf("the Fulminate gateway could not complete the account check: %s", parsed.ErrorDescription), false
		}
	}
	return "", false
}
