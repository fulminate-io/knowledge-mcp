// SPDX-License-Identifier: Apache-2.0

// Package tools — provider registry seed for log_backend manage tests.
//
// The configure_log_backend validator rejects unknown providers via the
// logwire registry. In production, init() blocks in collector/logs/loki,
// collector/logs/cloudwatch, and collector/logs/k8s register the real
// providers from cmd/knowledge/main.go's import set. Test binaries
// don't inherit those imports automatically — blank-importing the
// packages here pins the real names into the global registry so the
// moved manage tests (and any future log_backend test) can use them
// without hitting the unknown-provider gate.
//
// _test.go suffix means production binaries don't pay the cost; the
// linker drops these imports outside `go test`.

package tools

import (
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/cloudwatch"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/k8s"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/loki"
)
