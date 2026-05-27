// SPDX-License-Identifier: Apache-2.0

// Command knowledge is the MCP stdio client for the knowledge graph
// server. It speaks JSON-RPC over stdin/stdout (the MCP wire format),
// ships tools/list locally from static metadata, and proxies tools/call
// requests to a running knowledge-server on the configured TCP port.
// The server is NOT auto-started — bring the knowledge-server binary
// up in a separate process or service manager before configuring this
// binary in .mcp.json.
//
// Usage:
//
//	go build -o bin/knowledge .
//	./bin/knowledge --root .
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	// Register cloud collectors via blank imports (each has an init()
	// calling collector.Register + postpopulate.Register).
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud/aws"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud/azure"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud/gcp"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud/k8s"

	// Register CI/CD collectors via blank imports.
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/bitbucket"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/github"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cicd/gitlab"

	// Register web collector via blank import.
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/web"

	// Register log providers via blank imports.
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/cloudwatch"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/k8s"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/loki"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/stackdriver"

	// Register code graph collector (codesync.init registers "code" via
	// collector.Register + the postpopulate registry). codesync pulls
	// parser + tree-sitter transitively — that's the client-side path.
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/codesync"

	// Register tree-sitter parsers (parser.Populate walks this).
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"

	// Register LLM providers via blank imports. The dream Runner runs
	// client-side now; each sub-package's init() calls llm.RegisterProvider
	// so domains/dream's runReAct can resolve a worker's provider via
	// llm.NewClient. anthropic / openai / gemini drive tool-use via the
	// in-Go eino ReAct loop; claude-cli / codex-cli drive tool-use via
	// --mcp-config (see domains/llm/claudecli/translate.go::buildMCPConfig).
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/anthropic"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/claudecli"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/codexcli"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/gemini"
	_ "github.com/fulminate-io/knowledge-mcp/internal/llm/openai"

	"github.com/fulminate-io/knowledge-mcp/internal/bootstrap"
)

// version is the binary version, injected at build time via:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "dev"

// Publish the binary-specific version into the bootstrap package so
// the startup slog.Info reports the right SHA/tag. Mirrors
// cmd/knowledge-server/main.go's init().
func init() { bootstrap.Version = version }

func main() {
	// Subcommand dispatch runs BEFORE ParseFlags so each subcommand can
	// parse its own minimal flag set without colliding with the MCP
	// stdio flag list. Unknown first args (or no arg at all) fall
	// through to the default MCP stdio path.
	if handled, code := bootstrap.RunSubcommand(); handled {
		os.Exit(code)
	}
	if handled, code := bootstrap.RunWorkerSubcommand(); handled {
		os.Exit(code)
	}
	cfg, err := bootstrap.ParseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := bootstrap.Run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
