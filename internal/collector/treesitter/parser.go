// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"errors"
	"fmt"
	"sync"

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

// luaParseMu serializes every lua parse in the process.
//
// The vendored lua grammar's external scanner allocates no per-parser payload
// — its create() hook returns NULL — and keeps its entire lexer state (the
// pending quoted string's terminating character, and the long-bracket level
// count) in file-scope C variables. That state is therefore one instance per
// PROCESS, shared by every parser on every goroutine, and it is written for
// every ordinary quoted string, not just long strings. Two concurrent lua
// parses overwrite each other's lexer state mid-parse and return structurally
// different trees for identical input, with no error and no skip. Go's race
// detector cannot observe this because the state lives in C.
//
// Giving each worker its own Parser does not help, since the corrupted state
// is not in the parser. Serializing the parse is the only remedy available
// from this side, and it is deliberately scoped to lua alone: every other
// language parses concurrently as before.
//
// This lock is permanent, not a stopgap awaiting a routine dependency bump.
// Upstream tree-sitter-lua did fix this at v0.3.0 by moving the scanner state
// into a per-payload struct (still LANGUAGE_VERSION 14, ABI-compatible with the
// vendored core), but no Go binding ships the fixed scanner. smacker/go-tree-sitter
// is unmaintained and @latest resolves to the exact revision go.mod already
// pins, so no bump delivers it; the maintained official binding cannot co-link
// with smacker (duplicate tree-sitter core symbols), so a lua-only migration
// does not exist; and taking v0.3.0 by hand would move lua node kinds because it
// renames its external token set. The lock therefore stays until someone makes
// the vendoring decision — evaluating a move of every grammar to the maintained
// upstream bindings is tracked separately. scripts/lua_scanner_state_check.sh is
// the watchdog that fires if the vendored scanner ever stops holding file-scope
// state, which would mean the module was un-abandoned and this ruling is worth
// revisiting.
var luaParseMu sync.Mutex

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
//
// This is the single point through which all parsing flows, which is why the
// lua serialization described on luaParseMu lives here rather than at each
// caller. Non-lua languages pay one comparison and take no lock.
func (p *Parser) Parse(ctx context.Context, src []byte, lang Language) (*sitter.Tree, error) {
	entry, ok := registry[lang]
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}
	if lang == LangLua {
		luaParseMu.Lock()
		defer luaParseMu.Unlock()
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
