// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_testonly_test.go holds IAM rule helpers that are only exercised
// from the same-package test files. Moved here (from iam_rules.go) so
// `deadcode ./...` does not flag them as unreachable false positives.

// lookupIAMRule returns a rule by name. Used by tests to pin the registered
// set without exporting the map. Production code goes through
// lookupIAMRuleEntry (kept in iam_rules.go) which returns the full entry
// including confidence and actions.
func lookupIAMRule(name string) (iamRule, bool) {
	iamRulesMu.RLock()
	defer iamRulesMu.RUnlock()
	r, ok := iamRules[name]
	if !ok {
		return nil, false
	}
	return r.Fn, true
}
