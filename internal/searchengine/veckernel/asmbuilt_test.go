// SPDX-License-Identifier: Apache-2.0

//go:build !veckernel_noasm

package veckernel

// asmCompiledIn is the EXTERNAL expectation the dispatch test grades against:
// whether this build was compiled with assembly available at all.
//
// It has to come from a build tag rather than from the package's own state.
// Deriving it from len(asmArms()) or from ASMAvailable() would be an identity
// check — the dispatcher supplying the answer key to its own exam — and would
// pass whether dispatch worked or not. The build tag is the one fact about this
// build that the code under test does not get to decide.
const asmCompiledIn = true
