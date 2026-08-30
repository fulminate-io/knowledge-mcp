// SPDX-License-Identifier: Apache-2.0

// Command avogen writes the amd64 assembly for package veckernel: an AVX2/FMA
// tier and an AVX-512 tier, each in the two kernel shapes the traversal has —
// one vector against one, and one query fused against four candidate rows.
//
// # Why this is a separate module
//
// avo is a GENERATOR-ONLY dependency and the generated .s and stub .go files
// are COMMITTED, so an ordinary build, test or `go install` of the client never
// downloads avo at all. A nested module is what makes that true rather than
// merely intended: the go tool skips any directory containing its own go.mod
// when it expands ./..., so cmd/knowledge's build, vet, lint and test graphs
// cannot see this program and cmd/knowledge/go.mod never carries avo.
//
// Putting the generator in the veckernel package instead would not hold. A file
// carrying a build constraint is invisible to `go mod tidy`, which would drop
// the avo requirement from cmd/knowledge/go.mod on the next tidy and leave the
// generator unbuildable; keeping it un-constrained would put avo in the client
// binary's dependency graph for a program that never runs at runtime.
//
// # Running it
//
//	cd avogen && go run . -out ../dot_avx_amd64.s -stubs ../dot_avx_amd64.go -pkg veckernel
//
// or, equivalently, through the tag-guarded directive in the parent package:
//
//	go generate -tags veckernel_avogen ./internal/searchengine/veckernel/
//
// # What the generated kernels must satisfy
//
// The output is graded by the veckernel suite, not by inspection: agreement
// against a float64 oracle and against the portable reference, tail exhaustion
// over every dim 1..300, the value-domain cases (zeros, subnormals, non-finite
// propagation, sub-slice alignment) and the fuzz corpus. Regenerating and
// committing without running that suite on amd64 hardware is the one thing this
// package's whole design exists to prevent.
package main
