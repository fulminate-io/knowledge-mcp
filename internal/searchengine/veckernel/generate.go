// SPDX-License-Identifier: Apache-2.0

//go:build veckernel_avogen

package veckernel

// generate.go carries the directive that regenerates the amd64 assembly, and it
// sits behind a build tag ON PURPOSE.
//
// `go generate ./...` over the client module runs on every commit that touches
// cmd/knowledge — the docs-gen pre-commit hook does exactly that. go generate
// only scans files the current build context selects, so an untagged directive
// here would make avo a download every one of those commits needs, and would
// silently re-emit and re-stage the assembly on machines where nobody asked for
// it. The committed .s file is the artifact; regenerating it is a deliberate act
// with a review of the diff attached, not a side effect of committing.
//
// Regenerate with:
//
//	go generate -tags veckernel_avogen ./internal/searchengine/veckernel/
//
// then run the full suite ON amd64 HARDWARE before committing the result. The
// arm64 machine this package was written on cannot grade these kernels: it
// compiles them and runs none of them.
//
//go:generate sh -c "cd avogen && GOWORK=off go run . -out ../dot_avx_amd64.s -stubs ../dot_avx_amd64.go -pkg veckernel"
