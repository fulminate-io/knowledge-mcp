// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"errors"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"
)

// parseOperationLimitMicros caps tree-sitter parsing wall-clock time per
// file. The C / C++ grammars have known pathological backtracking on very
// large preprocessor-heavy files (e.g. redis/src/module.c, ~630KB) that can
// hang ts_parser_parse_string indefinitely. tree-sitter's ParseCtx does NOT
// honor context.Context cancellation in the smacker version we use — the
// only way to bound parser wall-clock is via SetOperationLimit (microseconds).
// 30s is generous for any reasonable source file but bounded enough to fail
// fast on pathological input.
const parseOperationLimitMicros = 30 * 1_000_000

// ErrParseTimeout is returned when the parser hits the operation limit.
var ErrParseTimeout = errors.New("treesitter: parser operation limit hit")

// Parser wraps tree-sitter to parse source files into ASTs.
type Parser struct {
	parser *sitter.Parser
}

// NewParser creates a new Parser.
func NewParser() *Parser {
	p := sitter.NewParser()
	p.SetOperationLimit(parseOperationLimitMicros)
	return &Parser{parser: p}
}

// Close releases tree-sitter resources. Must be called when done.
func (p *Parser) Close() {
	p.parser.Close()
}

// Parse parses source code for the given language and returns the AST tree.
// Returns an error if the language is unsupported, the operation limit is
// hit, or parsing fails.
func (p *Parser) Parse(ctx context.Context, src []byte, lang Language) (*sitter.Tree, error) {
	entry, ok := registry[lang]
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}
	p.parser.SetLanguage(entry.lang)
	tree, err := p.parser.ParseCtx(ctx, nil, src)
	if err != nil {
		if errors.Is(err, sitter.ErrOperationLimit) {
			return nil, fmt.Errorf("%w (lang=%s, %d bytes)", ErrParseTimeout, lang, len(src))
		}
		return nil, fmt.Errorf("parse failed: %w", err)
	}
	return tree, nil
}
