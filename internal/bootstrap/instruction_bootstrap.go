// SPDX-License-Identifier: Apache-2.0

// Package bootstrap — runInstructionBootstrap seeds the knowledge graph
// with agent + skill nodes parsed from `.claude/agents/*.md` and
// `.claude/skills/*.md` under rootDir. FUL-246 Phase 3c relocates this
// from the server (cmd/knowledge-server/bootstrap/server.go::buildServer
// previously called projects.Bootstrap) to the client because the
// server is now filesystem-blind for source paths (FUL-241) and the
// client owns disk I/O for code-graph + project assets.
//
// The bootstrap is gated by a query(type:agent, limit:1) pre-flight —
// when any agent node already exists, the function returns without
// issuing any create_batch. This makes startup idempotent across
// restarts without taking a global "bootstrap_done" graph-meta key
// (the server's metadata API is server-internal and not surfaced over
// the wire).

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// instructionBootstrapGC is the narrow MCP surface this bootstrap needs.
// The reads/writes ride the Execute carrier seam (T-GTB6): the create_batch
// seed and the idempotency pre-flight both compile to a declarative
// ExecuteRequest via engine.Compile and run through Execute. Structural typing
// makes it satisfied by the production graphClientCaller and a test fake.
// The bootstrap routes exclusively through Execute.
type instructionBootstrapGC interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// runInstructionBootstrap reads .claude/agents/*.md + .claude/skills/*.md
// under rootDir and posts the parsed nodes via a single
// mutate(create_batch) RPC with a shared bundle_id. Idempotent: a
// pre-flight query(type:agent, limit:1) returns early when any agent
// node already exists.
//
// Errors during the file read / parse phase are logged and the file is
// skipped. Errors during the gc.Call are returned to the caller for
// upstream handling — the caller logs slog.Warn and continues serving;
// MCP startup never aborts on bootstrap failure.
func runInstructionBootstrap(ctx context.Context, gc instructionBootstrapGC, rootDir string) error {
	if gc == nil {
		return fmt.Errorf("instruction bootstrap: graph caller unavailable")
	}

	// Pre-flight: skip when any agent node already exists.
	if hasAgentNodes(ctx, gc) {
		return nil
	}

	var nodes []nodeCreateItem

	agentFiles, _ := filepath.Glob(filepath.Join(rootDir, ".claude", "agents", "*.md"))
	for _, path := range agentFiles {
		if item, ok := readInstructionNodeItem(path, string(kgtypes.NodeAgent)); ok {
			nodes = append(nodes, item)
		}
	}

	skillFiles, _ := filepath.Glob(filepath.Join(rootDir, ".claude", "skills", "*.md"))
	for _, path := range skillFiles {
		if item, ok := readInstructionNodeItem(path, string(kgtypes.NodeSkill)); ok {
			nodes = append(nodes, item)
		}
	}

	if len(nodes) == 0 {
		slog.Info("bootstrap: no .claude/agents or .claude/skills files found — skipping")
		return nil
	}

	args, err := json.Marshal(struct {
		Operation string            `json:"operation"`
		Nodes     []nodeCreateItem  `json:"nodes"`
		Edges     []json.RawMessage `json:"edges"`
		BundleID  string            `json:"bundle_id"`
	}{
		Operation: "create_batch",
		Nodes:     nodes,
		Edges:     nil,
		BundleID:  tools.NewBundleID(),
	})
	if err != nil {
		return fmt.Errorf("instruction bootstrap: marshal: %w", err)
	}
	// The create_batch lowers to MUTATION_KIND_CREATE (compileMutateCreate
	// carries bundle_id) and rides the Execute carrier seam.
	req, ok := engine.Compile("mutate", args)
	if !ok {
		return fmt.Errorf("instruction bootstrap: create_batch args not reducible to an ExecuteRequest")
	}
	if _, cerr := gc.Execute(ctx, req); cerr != nil {
		return fmt.Errorf("instruction bootstrap: %w", cerr)
	}
	slog.Info("bootstrap: seeded instruction nodes",
		"agents", len(agentFiles),
		"skills", len(skillFiles),
		"total", len(nodes),
	)
	return nil
}

// nodeCreateItem is the wire shape consumed by mutate(create_batch).
type nodeCreateItem struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Summary     string            `json:"summary"`
	Content     string            `json:"content"`
	Status      string            `json:"status"`
	Metadata    map[string]string `json:"metadata"`
}

// hasAgentNodes returns true when a query(type:agent, limit:1) returns any
// node. Idempotency gate. Routes through the Execute carrier seam: a
// type=agent browse compiled by engine.Compile("query"); the typed Nodes
// carrier (engine.DecodeNodes) carries the matched nodes, gated on len>0.
func hasAgentNodes(ctx context.Context, gc instructionBootstrapGC) bool {
	args, err := json.Marshal(struct {
		Type  string `json:"type"`
		Limit int    `json:"limit"`
	}{Type: string(kgtypes.NodeAgent), Limit: 1})
	if err != nil {
		return false
	}
	req, ok := engine.Compile("query", args)
	if !ok {
		return false
	}
	resp, err := gc.Execute(ctx, req)
	if err != nil {
		return false
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return false
	}
	return len(nodes) > 0
}

// readInstructionNodeItem reads a markdown file and builds a
// nodeCreateItem suitable for the create_batch wire envelope. Mirrors
// projects.readInstructionNode body byte-for-byte except for the
// wire-shape (string Type) instead of kgtypes.NodeType.
func readInstructionNodeItem(path, nodeType string) (nodeCreateItem, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("bootstrap: failed to read file", "path", path, "error", err)
		return nodeCreateItem{}, false
	}
	content := string(data)
	stem := strings.TrimSuffix(filepath.Base(path), ".md")

	fm, body, ok := parseInstructionFrontmatter(content)
	if ok {
		return nodeCreateItem{
			Type:        nodeType,
			Name:        stem,
			Summary:     fm.Description,
			Description: instructionFirstParagraph(body, 200),
			Content:     content,
		}, true
	}
	return nodeCreateItem{
		Type:        nodeType,
		Name:        stem,
		Description: instructionFirstParagraph(content, 200),
		Content:     content,
	}, true
}

// instructionFrontmatter is the YAML frontmatter block at the top of
// .claude/agents/*.md and .claude/skills/*.md files.
type instructionFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseInstructionFrontmatter splits a markdown file into
// (frontmatter, body, ok). Markers are `---` on their own line.
func parseInstructionFrontmatter(content string) (instructionFrontmatter, string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return instructionFrontmatter{}, content, false
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return instructionFrontmatter{}, content, false
	}
	yamlBlock := strings.Join(lines[1:closeIdx], "\n")
	var fm instructionFrontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return instructionFrontmatter{}, content, false
	}
	body := strings.Join(lines[closeIdx+1:], "\n")
	return fm, body, true
}

// instructionFirstParagraph extracts the first non-empty paragraph
// from markdown text, trimming it to at most maxLen characters.
func instructionFirstParagraph(text string, maxLen int) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}
		if len(line) > maxLen {
			line = line[:maxLen]
		}
		return line
	}
	return ""
}
