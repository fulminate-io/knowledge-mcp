// SPDX-License-Identifier: Apache-2.0

// Package bench holds the CLIENT side of the end-to-end collect bench: a
// build-tagged harness that runs the real codesync collector over this
// repository's own root and uploads the result through the real
// collector/remote UploadSink to a purpose-built no-daemon server helper.
//
// # THIS FILE IS LOAD-BEARING AND IS NOT DECORATION
//
// Every OTHER file in this package carries `//go:build collectbench`. An
// untagged `go test` against a package whose files are ALL tag-excluded does not
// quietly skip: it prints "build constraints exclude all Go files" and reports
// FAIL [setup failed]. A bench package holding only a tagged test file would
// therefore BREAK the default suite rather than stay out of it. This untagged,
// declaration-free file is what makes the same run report "no test files" and
// stay green. Do not add a build tag to it, and do not delete it.
//
// # RUNNING THE BENCH
//
// The harness is not runnable on its own — it needs an instrumented Postgres and
// a booted server helper, both of which the conductor script owns:
//
//	scripts/collect-bench.sh
//
// The script starts a dedicated, pinned Postgres 18 container with
// pg_stat_statements + auto_explain preloaded, builds and boots the
// `collectbench-serve` helper against it, exports the server URL, invokes this
// harness once per RUN, snapshots the statement census over psql between runs,
// and assembles docs/collect-bench.md.
//
// # WHAT THIS SIDE MAY OBSERVE
//
// Only what a CLIENT can observe: uploaded node/edge counts, chunk counts and
// wall time. Statistics capture is the script's job, over psql. No pgx and no
// testcontainers dependency may enter cmd/knowledge's module graph — pgx in the
// OSS-shipped client's go.mod would name the backend, against the
// backend-neutral OSS posture. That promise is a standing criterion, not a
// convention.
package bench
