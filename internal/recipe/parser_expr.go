// SPDX-License-Identifier: Apache-2.0

package recipe

// parseExpr is the expression entry point. Grammar:
//
//	expr          = regex_expr | primary_expr
//	regex_expr    = primary_expr "~=" REGEX
//	primary_expr  = var_path | lit | func_call | "(" expr ")"
//	var_path      = (VAR | IDENT) { "." IDENT }
//	func_call     = IDENT "(" [ expr { "," expr } ] ")"
//	lit           = STRING
//
// The `~=` operator is left-associative but single-use — `a ~= /x/ ~= /y/`
// is rejected by the grammar because the right-hand side of `~=` must be
// a REGEX literal, never another regex expression.
func (p *parser) parseExpr() (Expr, error) {
	lhs, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	opKind := p.peek().Kind
	if opKind != TokTildeEq && opKind != TokBangTilde {
		return lhs, nil
	}
	op := p.peek()
	p.consume()
	rt := p.peek()
	if rt.Kind != TokRegex {
		return nil, p.errorf(rt, "expected /pattern/ after %q, got %q", op.Value, rt.Value)
	}
	p.consume()
	return ExprRegex{
		LHS:     lhs,
		Pattern: rt.Value,
		Negate:  opKind == TokBangTilde,
		Pos:     op.Pos,
	}, nil
}

// parsePrimary handles the non-operator expression forms. Order of
// branches mirrors the grammar and the disambiguation rule: a bare
// IDENT followed by `(` is a function call; otherwise it's the head of
// a var_path.
func (p *parser) parsePrimary() (Expr, error) {
	t := p.peek()
	switch t.Kind {
	case TokString:
		p.consume()
		return ExprLit{Value: t.Value, Pos: t.Pos}, nil
	case TokVar:
		return p.parseVarPath()
	case TokIdent:
		// Function call vs. bare ident. Only `IDENT (` is a call.
		if p.lookahead(1).Kind == TokLParen {
			return p.parseFuncCall()
		}
		return p.parseVarPath()
	case TokLParen:
		p.consume()
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(TokRParen, "')'"); err != nil {
			return nil, err
		}
		return inner, nil
	}
	return nil, p.errorf(t, "unexpected token %q in expression", t.Value)
}

// parseVarPath reads a dotted-path expression starting with either a
// VAR (which keeps the leading "$" on the first path segment) or a
// bare IDENT. Every subsequent segment after a dot must be an IDENT.
func (p *parser) parseVarPath() (Expr, error) {
	head := p.consume()
	var path []string
	switch head.Kind {
	case TokVar:
		path = append(path, "$"+head.Value)
	case TokIdent:
		path = append(path, head.Value)
	default:
		return nil, p.errorf(head, "expected $var or identifier at start of path")
	}
	for p.peek().Kind == TokDot {
		p.consume()
		seg, err := p.expectIdent("field name after '.'")
		if err != nil {
			return nil, err
		}
		path = append(path, seg.Value)
	}
	if len(path) == 1 && head.Kind == TokVar {
		return ExprVar{Name: head.Value, Pos: head.Pos}, nil
	}
	return ExprField{Path: path, Pos: head.Pos}, nil
}

// parseFuncCall expects to be positioned at an IDENT with a '(' in
// lookahead. Consumes both, parses a comma-separated argument list,
// then requires a matching ')'. Empty arg lists are legal.
func (p *parser) parseFuncCall() (Expr, error) {
	name := p.consume()
	if err := p.expect(TokLParen, "'('"); err != nil {
		return nil, err
	}
	var args []Expr
	if p.peek().Kind != TokRParen {
		for {
			arg, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			if p.peek().Kind != TokComma {
				break
			}
			p.consume()
		}
	}
	if err := p.expect(TokRParen, "')'"); err != nil {
		return nil, err
	}
	return ExprFunc{Name: name.Value, Args: args, Pos: name.Pos}, nil
}

// lookahead returns the token n positions ahead of the cursor without
// consuming. Used by parsePrimary to disambiguate IDENT(... from
// IDENT by itself.
func (p *parser) lookahead(n int) Token {
	idx := p.pos + n
	if idx >= len(p.tokens) {
		return Token{Kind: TokEOF}
	}
	return p.tokens[idx]
}
