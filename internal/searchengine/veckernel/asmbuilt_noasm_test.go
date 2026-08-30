// SPDX-License-Identifier: Apache-2.0

//go:build veckernel_noasm

package veckernel

// asmCompiledIn is false under the veckernel_noasm build tag: the assembly is
// not in this binary. See the doc on the other half of this pair.
const asmCompiledIn = false
