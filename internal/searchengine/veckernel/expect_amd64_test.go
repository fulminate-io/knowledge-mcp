// SPDX-License-Identifier: Apache-2.0

//go:build amd64 && !veckernel_noasm

package veckernel

import "golang.org/x/sys/cpu"

// amd64ExpectedTier derives, FROM CPUID VIA x/sys/cpu, which tier an amd64 build
// must prefer and whether ASMAvailable must be true.
//
// TWO CLAIMS, GRADED IN TWO DIFFERENT PLACES, and conflating them is how a
// preference test becomes an identity check. WHICH TIERS THIS CPU CAN RUN is a
// hardware fact, read here independently of the dispatcher's own gate — that is
// what this function asserts, and a typo in either gate makes the two disagree.
// WHICH OF TWO RUNNABLE TIERS IS FASTER is a measurement, and it is graded by
// TestDispatchPreferenceIsMeasured, which re-times both tiers on the host.
// Reading amd64PreferAVX512 below therefore checks the PLUMBING — constant to
// slice order to dispatch — and deliberately not the claim; a test that
// re-derived the claim from the same constant would agree with itself no matter
// which tier was actually quicker.
func amd64ExpectedTier() (tier string, asmAvailable bool) {
	hasAVX2 := cpu.X86.HasAVX && cpu.X86.HasAVX2 && cpu.X86.HasFMA
	hasAVX512 := cpu.X86.HasAVX512F && cpu.X86.HasFMA

	switch {
	case hasAVX512 && hasAVX2:
		if amd64PreferAVX512 {
			return TierAVX512, true
		}
		return TierAVX2, true
	case hasAVX512:
		return TierAVX512, true
	case hasAVX2:
		return TierAVX2, true
	default:
		// An amd64 part with neither feature set — pre-Haswell silicon, or a
		// hypervisor masking them. The reference runs and says so.
		return TierReference, false
	}
}
