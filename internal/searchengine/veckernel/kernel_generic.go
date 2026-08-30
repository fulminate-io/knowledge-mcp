// SPDX-License-Identifier: Apache-2.0

//go:build veckernel_noasm || (!arm64 && !amd64)

package veckernel

// kernel_generic.go covers two cases with one line:
//
//   - Any architecture with no assembly arm written for it. The reference runs,
//     Kernel() says so, and nothing silently claims a SIMD path.
//   - Any build carrying the veckernel_noasm tag, on ANY architecture. That tag
//     is the compile-time opt-out — distinct from the VECKERNEL_FORCE runtime
//     pin, which selects among the arms a build has. noasm removes the assembly
//     from the binary entirely, which is what a caller who cannot ship .s files
//     needs and what a bisect against a suspected assembly defect needs.
func asmArms() []arm { return nil }
