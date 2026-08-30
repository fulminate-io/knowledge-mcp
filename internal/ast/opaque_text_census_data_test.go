// SPDX-License-Identifier: Apache-2.0

// opaque_text_census_data_test.go — the span-gap census DATA: the verdict table
// classifying every measured kind, and the per-grammar probe sets the hermetic
// census parses. Split from opaque_text_census_test.go, which holds the
// measurement and the assertions, so neither file crosses the line cap and so a
// probe or verdict edit reads as a data change rather than a logic one.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// gapVerdicts is the classification, per language and kind. It is the census's
// judgement half — the measurement says WHICH kinds gap, and this says what the
// gapped bytes are. Rows are added only after reading the measured sample text;
// a row invented to silence a failure is the one thing this table must never be.
var gapVerdicts = map[treesitter.Language]map[string]gapVerdict{
	treesitter.LangBash: {
		"heredoc_body":         verdictOpaque,
		"array":                verdictContinuation,
		"binary_expression":    verdictContinuation,
		"case_item":            verdictContinuation,
		"command":              verdictContinuation,
		"command_substitution": verdictContinuation,
		"expansion":            verdictContinuation,
		"function_definition":  verdictContinuation,
		"list":                 verdictContinuation,
		"pipeline":             verdictContinuation,
	},
	treesitter.LangC: {
		"binary_expression":    verdictContinuation,
		"preproc_def":          verdictContinuation,
		"preproc_function_def": verdictContinuation,
	},
	treesitter.LangCSharp: {
		"interpolation_format_clause": verdictOpaque,
	},
	treesitter.LangElixir: {
		"quoted_keyword": verdictDelimiter,
	},
	treesitter.LangGo: {
		"interpreted_string_literal": verdictOpaque,
	},
	treesitter.LangElm: {
		"block_comment": verdictOpaque,
	},
	treesitter.LangGroovy: {
		"comment": verdictOpaque,
		// groovy_doc gaps on its ` * ` line decoration only: the doc's text is
		// covered by a first_line child and already constrains a match. Verified by
		// declaring it opaque and observing its regression pair pass with the
		// matcher's opaque comparison disabled — a green test over a property that
		// already held.
		"groovy_doc": verdictDelimiter,
		"string":     verdictDelimiter,
	},
	treesitter.LangKotlin: {
		"character_literal": verdictOpaque,
		"string_literal":    verdictDelimiter,
		"nullable_type":     verdictOperator,
		"statements":        verdictSeparator,
	},
	treesitter.LangLua: {
		"identifier_list": verdictSeparator,
		"parameter_list":  verdictSeparator,
	},
	treesitter.LangOCaml: {
		"string_content":        verdictOpaque,
		"quoted_string_content": verdictOpaque,
		"quoted_string":         verdictDelimiter,
	},
	treesitter.LangPython: {
		"string_content": verdictOpaque,
	},
	treesitter.LangRuby: {
		"assignment":     verdictContinuation,
		"call":           verdictContinuation,
		"chained_string": verdictContinuation,
	},
	treesitter.LangRust: {
		"block_comment":            verdictOpaque,
		"line_comment":             verdictOpaque,
		"raw_string_literal":       verdictDelimiter,
		"token_repetition":         verdictSeparator,
		"token_repetition_pattern": verdictSeparator,
	},
	treesitter.LangScala: {
		"block_comment":       verdictOpaque,
		"comment":             verdictOpaque,
		"interpolated_string": verdictOpaque,
	},
	treesitter.LangSwift: {
		"class_body": verdictSeparator,
		"statements": verdictSeparator,
	},
}

// errorGapKind is the parser's error-recovery node, exempted everywhere rather
// than per-language for the reason errorExtraKind is.
const errorGapKind = "ERROR"

// opaqueProbe is one grammar's probe SET, carrying that grammar's string,
// character, numeric, raw, escaped, empty, interpolated and comment forms so a
// kind that gaps in only one of them still surfaces. The measurement unions
// every snippet's gaps.
//
// A SET RATHER THAN ONE SNIPPET, for a measured reason. Written adjacently,
// tree-sitter-groovy absorbs a following block comment — and the statement
// between them — into the preceding groovy_doc node, so a single combined
// snippet produces groovy_doc and never produces `comment` at all. Isolating the
// forms is what stops one construct's parse from hiding another's. The union
// mirrors layout_census_test.go's per-grammar pair sets.
type opaqueProbe struct {
	lang treesitter.Language
	srcs []string
}

var opaqueProbes = []opaqueProbe{
	{treesitter.LangBash, []string{
		"a=\"ZZALPHA\"\nb='ZZBETA'\nc=\"pre${a}post\"\nd=ZZBARE\ne=$(echo ZZSUB)\n",
		"# ZZCOMMENT\ncat <<EOF\nZZDOC ${a}\nEOF\n",
	}},
	{treesitter.LangC, []string{
		"const char *a = \"ZZALPHA\";\nchar b = 'q';\nint c = 12345;\nconst char *d = \"ZZESC\\nTAIL\";\nchar e = '\\n';\nconst char *f = \"\";\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nint g = 1;\n",
	}},
	{treesitter.LangCPP, []string{
		"const char *a = \"ZZALPHA\";\nchar b = 'q';\nint c = 12345;\nauto d = R\"(ZZRAW)\";\nconst char *e = \"ZZESC\\nTAIL\";\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nint g = 1;\n",
	}},
	{treesitter.LangCSharp, []string{
		"class C { string a = \"ZZALPHA\"; char b = 'q'; int c = 12345; string e = @\"ZZVERB\"; string f = \"ZZESC\\nTAIL\"; char g = '\\n'; string h = \"\"; }\n",
		"class D { string d = $\"pre{a:yyyyMM}post\"; }\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nclass E {}\n",
	}},
	{treesitter.LangElixir, []string{
		"a = \"ZZALPHA\"\nb = 12345\nc = \"pre#{a}post\"\nd = [zzk: 1, \"zzq\": 2]\ne = \"ZZESC\\nTAIL\"\nf = ~s(ZZSIGIL)\ng = 'ZZCHARLIST'\nh = \"\"\n",
		"# ZZCOMMENT\ni = 1\n",
	}},
	{treesitter.LangElm, []string{
		"module M exposing (..)\n\na = \"ZZALPHA\"\nb = 12345\nc = 'q'\nd = \"ZZESC\\nTAIL\"\ne = \"\"\"ZZTRIPLE\"\"\"\nf = \"\"\n",
		"module M exposing (..)\n\n-- ZZLINE\n\n{- ZZBLOCK -}\ng = 1\n",
	}},
	{treesitter.LangGo, []string{
		"package p\n\nvar a = \"ZZALPHA\"\nvar b = `ZZBETA`\nvar c = 'q'\nvar d = 12345\nvar e = 1.5\nvar f = \"ZZESC\\nTAIL\"\nvar g = '\\n'\nvar h = \"\"\nvar i = 0x1f\n",
		"package p\n\n// ZZLINE\n/*\n * ZZBLOCK\n */\nvar j = 1\n",
	}},
	{treesitter.LangGroovy, []string{
		"def a = \"ZZALPHA\"\ndef b = 'ZZBETA'\ndef c = 12345\ndef d = \"pre${a}post\"\ndef e = \"ZZESC\\nTAIL\"\ndef f = '''ZZTRIPLE'''\ndef g = \"\"\n",
		"def h = 1\n/*\n * ZZBLOCK\n */\ndef i = 2\n",
		"/**\n * ZZDOC\n */\ndef j = 3\n",
	}},
	{treesitter.LangJava, []string{
		"class C { String a = \"ZZALPHA\"; char b = 'q'; int c = 12345; String d = \"\"\"\nZZTEXT\"\"\"; String e = \"ZZESC\\nTAIL\"; char f = '\\n'; String g = \"\"; }\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nclass D {}\n",
	}},
	{treesitter.LangJavaScript, []string{
		"var a = \"ZZALPHA\";\nvar b = 'ZZBETA';\nvar c = `ZZGAMMA ${a} tail`;\nvar d = 12345;\nvar e = /ZZRE/g;\nvar f = \"ZZESC\\nTAIL\";\nvar g = '';\nvar h = `plain`;\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nvar i = 1;\n",
	}},
	{treesitter.LangKotlin, []string{
		"fun f(p: String?) { val a = \"ZZALPHA\"; val b = 'q'; val c = 12345; val d = \"pre${a}post\"; val e = \"ZZESC\\nTAIL\"; val g = '\\n'; val h = \"\" }\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nval i = 1\n",
	}},
	{treesitter.LangLua, []string{
		"local a = \"ZZALPHA\"\nlocal b = 'ZZBETA'\nlocal c = 12345\nlocal d = [[ZZLONG]]\nlocal e = \"ZZESC\\nTAIL\"\nlocal f = \"\"\nlocal function g(x, y) return x, y end\n",
		"-- ZZLINE\n--[[\n ZZBLOCK\n]]\nlocal h = 1\n",
	}},
	{treesitter.LangOCaml, []string{
		"let a = \"ZZALPHA\"\nlet b = 12345\nlet c = 'q'\nlet d = \"ZZESC\\nTAIL\"\nlet e = \"\"\nlet f = {|ZZQUOTED|}\nlet g = {|ZZPRE %s ZZPOST|}\n",
		"(* ZZCOMMENT *)\nlet h = 1\n",
	}},
	{treesitter.LangPython, []string{
		"a = \"ZZALPHA\"\nb = 'ZZBETA'\nc = f\"pre{a}post\"\nd = 12345\ne = b\"ZZBYTES\"\nf = \"\"\"ZZTRIPLE\"\"\"\ng = \"ZZESC\\nTAIL\"\nh = r'ZZRAW'\ni = \"\"\n",
		"# ZZCOMMENT\nj = 1\n",
	}},
	{treesitter.LangRuby, []string{
		"a = \"ZZALPHA\"\nb = 'ZZBETA'\nc = \"pre#{a}post\"\nd = 12345\ne = :zzsym\nf = \"ZZESC\\nTAIL\"\ng = %w[zz yy]\nh = /ZZRE/\ni = \"\"\n",
		"# ZZCOMMENT\nj = 1\n",
	}},
	{treesitter.LangRust, []string{
		"fn f() { let a = \"ZZALPHA\"; let b = r\"ZZBETA\"; let c = 'q'; let d = 12345; let e = b\"ZZBYTE\"; let g = \"ZZESC\\nTAIL\"; let h = '\\n'; let i = \"\"; let j = r#\"ZZHASH\"#; }\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nfn g() {}\n",
	}},
	{treesitter.LangScala, []string{
		"object O { val a = \"ZZALPHA\"; val b = 'q'; val c = 12345; val d = s\"pre${a}post\"; val e = \"ZZESC\\nTAIL\"; val g = '\\n'; val h = \"\" }\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nobject P\n",
	}},
	{treesitter.LangSwift, []string{
		"let a = \"ZZALPHA\"\nlet b = 12345\nlet c = \"pre\\(a)post\"\nlet d = \"ZZESC\\nTAIL\"\nlet e = \"\"\nfunc f() { let x = 1; let y = 2 }\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nlet g = 1\n",
	}},
	{treesitter.LangTSX, []string{
		"let a = \"ZZALPHA\";\nlet b = 'ZZBETA';\nlet c = `ZZGAMMA ${a} tail`;\nlet d = 12345;\nlet f = \"ZZESC\\nTAIL\";\nlet g = '';\nlet i = <A b=\"ZZATTR\" />;\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nlet j = 1;\n",
	}},
	{treesitter.LangTypeScript, []string{
		"let a = \"ZZALPHA\";\nlet b = 'ZZBETA';\nlet c = `ZZGAMMA ${a} tail`;\nlet d = 12345;\nlet e = /ZZRE/g;\nlet f = \"ZZESC\\nTAIL\";\nlet g = '';\n",
		"// ZZLINE\n/*\n * ZZBLOCK\n */\nlet h = 1;\n",
	}},
}
