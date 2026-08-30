// SPDX-License-Identifier: Apache-2.0

//go:build !amd64 || veckernel_noasm

package veckernel

// amd64ExpectedTier has no answer on a build that carries no amd64 tiers, so it
// PANICS rather than returning a plausible one.
//
// The shared dispatch test names it inside a `case "amd64"` that a build without
// amd64 assembly cannot reach — the veckernel_noasm branch returns before the
// GOARCH switch, and no other architecture enters that case. This half of the
// pair exists so that file compiles everywhere, and it panics because the
// alternative is worse: returning TierReference here would be a silently wrong
// expectation the moment one of those guards changed, and a test asserting
// against a wrong expectation is a test that grades nothing while passing.
func amd64ExpectedTier() (tier string, asmAvailable bool) {
	panic("veckernel: amd64ExpectedTier consulted on a build with no amd64 assembly tiers — " +
		"the caller's build-configuration guard has been broken")
}
