// SPDX-License-Identifier: Apache-2.0

// Package bootstrap — runInstructionBootstrap seeds the knowledge graph
// with agent + skill nodes parsed from `.claude/agents/*.md` and
// `.claude/skills/*/SKILL.md` under rootDir. This was relocated
// from the server (cmd/knowledge-server/bootstrap/server.go::buildServer
// previously called projects.Bootstrap) to the client because the
// server is now filesystem-blind for source paths and the
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

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	"gopkg.in/yaml.v3"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// instructionBootstrapGC is the narrow MCP surface this bootstrap needs.
// The reads/writes ride the Execute carrier seam: the create_batch
// seed and the idempotency pre-flight both compile to a declarative
// ExecuteRequest via engine.Compile and run through Execute. Structural typing
// makes it satisfied by the production graphClientCaller and a test fake.
// The bootstrap routes exclusively through Execute.
type instructionBootstrapGC interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// runInstructionBootstrap reads .claude/agents/*.md + .claude/skills/*/SKILL.md
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

	// One-shot boot-time work with no originating tool call: stamp the
	// query-origin operation ONCE here so every graph call below inherits it.
	// The pre-flight is the call that must not be missed — on an
	// already-seeded graph it is the only one the bootstrap makes.
	ctx = graphclient.WithOperation(ctx, graphclient.OpInstructionBootstrap)

	// Pre-flight: skip when any agent node already exists.
	if hasAgentNodes(ctx, gc) {
		return nil
	}

	var nodes []nodeCreateItem

	agentFiles, _ := filepath.Glob(filepath.Join(rootDir, ".claude", "agents", "*.md"))
	for _, path := range agentFiles {
		if item, ok := readInstructionNodeItem(path, string(kgtypes.NodeAgent), strings.TrimSuffix(filepath.Base(path), ".md")); ok {
			nodes = append(nodes, item)
		}
	}

	// NESTED layout: .claude/skills/<name>/SKILL.md. A flat
	// .claude/skills/*.md glob matched ZERO files in the shipped tree, so
	// no skill node was ever seeded — skill recall found nothing and the
	// agent->skill edge could never resolve. The name comes from the
	// DIRECTORY because every file is called SKILL.md; deriving it from
	// the filename stem would name every skill "SKILL".
	skillFiles, _ := filepath.Glob(filepath.Join(rootDir, ".claude", "skills", "*", "SKILL.md"))
	for _, path := range skillFiles {
		name := filepath.Base(filepath.Dir(path))
		if item, ok := readInstructionNodeItem(path, string(kgtypes.NodeSkill), name); ok {
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
// readInstructionNodeItem reads one instruction file into a create item.
// The NAME is supplied by the caller rather than derived here, because the
// two layouts name a node differently: an agent is named after its file
// stem, a skill after its containing DIRECTORY (every skill file is
// called SKILL.md).
func readInstructionNodeItem(path, nodeType, name string) (nodeCreateItem, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("bootstrap: failed to read file", "path", path, "error", err)
		return nodeCreateItem{}, false
	}
	content := string(data)

	// THREE FILE SHAPES, and two of them are skipped rather than repaired. agent
	// and skill are embed-only-knowledge types, so a node with no summary is
	// refused by the server — and because every file rides ONE create_batch, that
	// refusal rolls back EVERY agent and skill node. One malformed file must cost
	// itself, not the whole seed, and nothing here composes a summary on the
	// file's behalf: the description is the author's to supply.
	//
	// parseInstructionFrontmatter reports ok for any well-formed delimited block
	// that unmarshals, and never inspects whether a description: key is present,
	// so the descriptionless-frontmatter shape needs its own gate below — it
	// reaches this point with ok true and an empty fm.Description.
	fm, body, ok := parseInstructionFrontmatter(content)
	if !ok {
		slog.Warn("bootstrap: skipping file with no frontmatter block — add a `---` delimited block with a `description:` key",
			"path", path)
		return nodeCreateItem{}, false
	}
	if strings.TrimSpace(fm.Description) == "" {
		slog.Warn("bootstrap: skipping file whose frontmatter has no `description:` key — it is the node's summary and nothing composes one for it",
			"path", path)
		return nodeCreateItem{}, false
	}
	return nodeCreateItem{
		Type:        nodeType,
		Name:        name,
		Summary:     fm.Description,
		Description: instructionFirstParagraph(body, 200),
		Content:     content,
	}, true
}

// instructionFrontmatter is the YAML frontmatter block at the top of
// .claude/agents/*.md and .claude/skills/*/SKILL.md files.
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
