// SPDX-License-Identifier: Apache-2.0

// doc.go carries the package-level //go:generate directive that drives the docs
// generator. `go generate ./...` run in the cmd/knowledge module reaches this
// directive and executes the thin main shim under ./cmd/docgen, which resolves
// the repo-root docs/guides tree and calls Generate.
//
//go:generate go run ./cmd/docgen

package docgen
