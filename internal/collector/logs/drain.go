// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// DrainConfig holds tuning parameters for the Drain algorithm.
type DrainConfig struct {
	// SimThreshold is the minimum similarity score to merge into a cluster.
	SimThreshold float64

	// MaxDepth is the depth of the prefix parse tree.
	MaxDepth int

	// MaxChildren is the maximum children per tree node before forcing wildcard.
	MaxChildren int

	// MaxClusters is the hard cap on the number of template clusters.
	MaxClusters int
}

// DefaultDrainConfig returns the default Drain algorithm parameters.
func DefaultDrainConfig() DrainConfig {
	return DrainConfig{
		SimThreshold: 0.4,
		MaxDepth:     4,
		MaxChildren:  100,
		MaxClusters:  200,
	}
}

// DrainEngine implements the Drain log parsing algorithm.
//
// Algorithm overview:
//  1. Pre-process: strip timestamps and high-cardinality tokens
//  2. Fixed-depth prefix parse tree (default depth=4)
//  3. Token-count branching at level 1, prefix-token at levels 2-N
//  4. Similarity scoring at leaf nodes (default threshold=0.4)
//  5. Template merging: replace differing tokens with <*>
type DrainEngine struct {
	root     *drainNode
	clusters []*drainCluster
	config   DrainConfig
}

// drainNode is an internal prefix tree node.
type drainNode struct {
	children map[string]*drainNode
	clusters []*drainCluster
}

// drainCluster pairs internal token state with the external wirelogs.LogTemplate.
type drainCluster struct {
	tokens   []string
	template *wirelogs.LogTemplate
}

// maxExampleVars caps the number of example variable sets stored per template.
const maxExampleVars = 3

// NewDrainEngine creates a DrainEngine with the given configuration.
func NewDrainEngine(cfg DrainConfig) *DrainEngine {
	return &DrainEngine{
		root:   &drainNode{children: make(map[string]*drainNode)},
		config: cfg,
	}
}

// AddMessage clusters a log entry into an existing or new template.
// Returns the matched wirelogs.LogTemplate, or nil if the message is empty.
func (d *DrainEngine) AddMessage(entry wirelogs.LogEntry) *wirelogs.LogTemplate {
	processed := PreProcess(entry.Message)
	tokens := Tokenize(processed)
	if len(tokens) == 0 {
		return nil
	}

	node := d.walkTree(tokens)
	cluster := d.findMatchingCluster(node, tokens)
	if cluster != nil {
		d.updateCluster(cluster, tokens, entry)
		return cluster.template
	}

	if len(d.clusters) >= d.config.MaxClusters {
		return d.handleOverflow(tokens, entry)
	}

	return d.createCluster(node, tokens, entry)
}

// Templates returns the current set of log templates.
func (d *DrainEngine) Templates() []*wirelogs.LogTemplate {
	out := make([]*wirelogs.LogTemplate, len(d.clusters))
	for i, c := range d.clusters {
		out[i] = c.template
	}
	return out
}

// walkTree traverses (or creates) the prefix tree path for tokens.
func (d *DrainEngine) walkTree(tokens []string) *drainNode {
	bucket := tokenCountBucket(len(tokens))
	node := d.getOrCreateChild(d.root, bucket)

	for depth := 0; depth < d.config.MaxDepth-1 && depth < len(tokens); depth++ {
		token := tokens[depth]
		if isWildcard(token) {
			token = Wildcard
		}
		node = d.getOrCreateChild(node, token)
	}
	return node
}

// handleOverflow merges into the best global match when at max clusters.
func (d *DrainEngine) handleOverflow(tokens []string, entry wirelogs.LogEntry) *wirelogs.LogTemplate {
	best := d.findBestGlobalMatch(tokens)
	if best == nil {
		best = d.clusters[len(d.clusters)-1]
	}
	d.updateCluster(best, tokens, entry)
	return best.template
}

// createCluster creates a new drainCluster and its wirelogs.LogTemplate.
func (d *DrainEngine) createCluster(node *drainNode, tokens []string, entry wirelogs.LogEntry) *wirelogs.LogTemplate {
	pattern := strings.Join(tokens, " ")
	tpl := &wirelogs.LogTemplate{
		ID:        templateID(pattern),
		Pattern:   pattern,
		Severity:  entry.Severity,
		Count:     1,
		FirstSeen: entry.Timestamp,
		LastSeen:  entry.Timestamp,
	}
	tpl.Alias = TemplateAliasFor(tpl)
	c := &drainCluster{tokens: tokens, template: tpl}
	node.clusters = append(node.clusters, c)
	d.clusters = append(d.clusters, c)
	return tpl
}

// updateCluster merges new tokens and updates the template metadata.
func (d *DrainEngine) updateCluster(c *drainCluster, tokens []string, entry wirelogs.LogEntry) {
	vars := extractVars(c.tokens, tokens)
	c.tokens = mergeTokens(c.tokens, tokens)
	pattern := strings.Join(c.tokens, " ")
	tpl := c.template
	tpl.Pattern = pattern
	tpl.ID = templateID(pattern)
	tpl.Count++
	updateTimeRange(tpl, entry.Timestamp)
	if severityRank(entry.Severity) > severityRank(tpl.Severity) {
		tpl.Severity = entry.Severity
	}
	// Pattern + severity may have shifted, so refresh the alias.
	tpl.Alias = TemplateAliasFor(tpl)
	if vars != nil && len(tpl.ExampleVars) < maxExampleVars {
		tpl.ExampleVars = append(tpl.ExampleVars, vars)
	}
}

func (d *DrainEngine) getOrCreateChild(parent *drainNode, key string) *drainNode {
	if child, ok := parent.children[key]; ok {
		return child
	}
	if len(parent.children) >= d.config.MaxChildren {
		if child, ok := parent.children[Wildcard]; ok {
			return child
		}
		key = Wildcard
	}
	child := &drainNode{children: make(map[string]*drainNode)}
	parent.children[key] = child
	return child
}

func (d *DrainEngine) findMatchingCluster(node *drainNode, tokens []string) *drainCluster {
	var best *drainCluster
	bestSim := d.config.SimThreshold
	for _, c := range node.clusters {
		sim := similarity(c.tokens, tokens)
		if sim > bestSim {
			bestSim = sim
			best = c
		}
	}
	return best
}

func (d *DrainEngine) findBestGlobalMatch(tokens []string) *drainCluster {
	var best *drainCluster
	bestSim := d.config.SimThreshold
	for _, c := range d.clusters {
		if len(c.tokens) != len(tokens) {
			continue
		}
		sim := similarity(c.tokens, tokens)
		if sim > bestSim {
			bestSim = sim
			best = c
		}
	}
	return best
}

func similarity(a, b []string) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	matches := 0
	for i := range a {
		if a[i] == b[i] || a[i] == Wildcard || b[i] == Wildcard {
			matches++
		}
	}
	return float64(matches) / float64(len(a))
}

func mergeTokens(template, tokens []string) []string {
	if len(template) != len(tokens) {
		return template
	}
	result := make([]string, len(template))
	for i := range template {
		if template[i] == tokens[i] || tokens[i] == Wildcard {
			result[i] = template[i]
		} else {
			result[i] = Wildcard
		}
	}
	return result
}

// templateID returns a stable sha256 hex hash of the pattern.
func templateID(pattern string) string {
	h := sha256.Sum256([]byte(pattern))
	return fmt.Sprintf("%x", h[:16])
}

// extractVars returns the tokens at wildcard positions, or nil if none.
func extractVars(templateTokens, msgTokens []string) []string {
	if len(templateTokens) != len(msgTokens) {
		return nil
	}
	var vars []string
	for i, t := range templateTokens {
		if t == Wildcard && msgTokens[i] != Wildcard {
			vars = append(vars, msgTokens[i])
		}
	}
	return vars
}

// updateTimeRange adjusts FirstSeen and LastSeen on the template.
func updateTimeRange(tpl *wirelogs.LogTemplate, ts time.Time) {
	if !ts.IsZero() {
		if tpl.FirstSeen.IsZero() || ts.Before(tpl.FirstSeen) {
			tpl.FirstSeen = ts
		}
		if ts.After(tpl.LastSeen) {
			tpl.LastSeen = ts
		}
	}
}

// severityRank returns a numeric rank for severity comparison.
// Higher rank = more severe. Unknown severities rank 0.
func severityRank(sev string) int {
	switch strings.ToUpper(sev) {
	case "TRACE":
		return 1
	case "DEBUG":
		return 2
	case "INFO":
		return 3
	case "WARN", "WARNING":
		return 4
	case "ERROR":
		return 5
	case "FATAL", "CRITICAL", "EMERGENCY":
		return 6
	default:
		return 0
	}
}
