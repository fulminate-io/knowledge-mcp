// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// WorkOS permission slugs. These identify capabilities the issued JWT
// carries; the agent's Bearer middleware enforces them at the resource
// endpoint. Values must match the agent's bearer.PermXxx constants
// exactly. Changing any
// value here is a wire break that requires coordinated agent + WorkOS
// dashboard updates.
const (
	PermMCPKnowledgeRead  = "mcp:knowledge:read"
	PermMCPKnowledgeWrite = "mcp:knowledge:write"
	PermOrgSSO            = "org:sso"
	PermOrgAdvancedRBAC   = "org:advanced_rbac"
	PermDeployBYOC        = "deploy:byoc"
)

// PermissionSet is a case-sensitive set of WorkOS permission slugs. Zero
// value is usable as an empty set: every Has() lookup returns false.
// Construct via a composite literal or via [ParsePermissionsFromJWT].
type PermissionSet map[string]struct{}

// Has reports whether the given permission is in the set. Safe on nil
// receivers.
func (p PermissionSet) Has(perm string) bool {
	if p == nil {
		return false
	}
	_, ok := p[perm]
	return ok
}

// List returns the permissions in sorted order. Useful for deterministic
// logging and status reporting. Returns an empty (non-nil) slice if the
// set is empty.
func (p PermissionSet) List() []string {
	out := make([]string, 0, len(p))
	for k := range p {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ParsePermissionsFromJWT extracts the `permissions` claim and `exp`
// claim from the given WorkOS access token WITHOUT verifying the
// signature. The client trusts tokens persisted in its keychain because
// they reached the keychain only via a successful AuthKit exchange;
// signature verification is the agent's responsibility on every API
// call.
//
// WorkOS emits `permissions` as a JSON array of strings (per the WorkOS
// Roles+Permissions doc); a token with no `permissions` claim yields an
// empty set (not an error). A token with no `exp` claim yields a zero
// time.
func ParsePermissionsFromJWT(tokenString string) (PermissionSet, time.Time, error) {
	parser := jwt.NewParser()
	claims := jwt.MapClaims{}
	if _, _, err := parser.ParseUnverified(tokenString, claims); err != nil {
		return nil, time.Time{}, fmt.Errorf("auth: parse JWT: %w", err)
	}
	return extractPermissions(claims["permissions"]), parseJWTExp(claims), nil
}

// extractPermissions decodes the `permissions` claim into a PermissionSet.
// WorkOS encodes it as a JSON array of strings; non-WorkOS issuers
// sometimes use a single JSON-array-encoded string or a space-separated
// list — accept both defensively. Unknown shapes yield an empty set.
func extractPermissions(raw any) PermissionSet {
	perms := make(PermissionSet)
	switch v := raw.(type) {
	case []any:
		addArrayPerms(perms, v)
	case string:
		addStringPerms(perms, v)
	}
	return perms
}

// addArrayPerms inserts string entries from a JSON array into perms.
// Non-string entries and empty strings are ignored.
func addArrayPerms(perms PermissionSet, arr []any) {
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			perms[s] = struct{}{}
		}
	}
}

// addStringPerms handles the fallback shapes: either a JSON-encoded
// array smuggled into a string claim, or a plain space-separated list.
func addStringPerms(perms PermissionSet, v string) {
	var arr []string
	if err := json.Unmarshal([]byte(v), &arr); err == nil {
		for _, s := range arr {
			if s != "" {
				perms[s] = struct{}{}
			}
		}
		return
	}
	for _, s := range splitSpace(v) {
		if s != "" {
			perms[s] = struct{}{}
		}
	}
}

// ParseAccountIDFromJWT extracts the WorkOS organization_id claim (the
// Fulminate per-org account binding) or the `sub` claim (consumer
// per-user account binding) from the given JWT WITHOUT verifying the
// signature. Returns "" (not an error) when neither claim is present so
// callers can treat "" as "account binding unknown, skip the check".
//

// parseJWTExp extracts the `exp` claim from a MapClaims instance. JWT
// libraries decode numeric dates as float64 (seconds since epoch); some
// encoders use json.Number. Return zero time if the claim is absent or
// malformed — callers treat zero expiry as "no cache hint".
func parseJWTExp(claims jwt.MapClaims) time.Time {
	raw, ok := claims["exp"]
	if !ok {
		return time.Time{}
	}
	switch v := raw.(type) {
	case float64:
		return time.Unix(int64(v), 0)
	case int64:
		return time.Unix(v, 0)
	case int:
		return time.Unix(int64(v), 0)
	}
	return time.Time{}
}

// splitSpace splits s by whitespace runs, dropping empty entries.
// Defensive helper used by ParsePermissionsFromJWT to accept a space-
// separated `permissions` claim shape from non-WorkOS issuers.
func splitSpace(s string) []string {
	var out []string
	start := -1
	for i := 0; i < len(s); i++ {
		isSpace := s[i] == ' ' || s[i] == '\t' || s[i] == '\n'
		switch {
		case !isSpace && start < 0:
			start = i
		case isSpace && start >= 0:
			out = append(out, s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}
