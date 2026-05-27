// SPDX-License-Identifier: Apache-2.0

package exposure

// resetSeedRegistryForTest clears the seed registry between subtests.
func resetSeedRegistryForTest() {
	seedRegistryMu.Lock()
	defer seedRegistryMu.Unlock()
	seedRegistry = map[string]seedRuleEntry{}
}

// resetSensitiveRegistryForTest clears the sensitive-rule registry between
// subtests so panic-on-duplicate semantics can be re-verified without
// leaving the registry mutated.
func resetSensitiveRegistryForTest() {
	sensitiveRegistryMu.Lock()
	defer sensitiveRegistryMu.Unlock()
	sensitiveRegistry = map[string]SensitiveRule{}
}
