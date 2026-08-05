// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"strings"
	"testing"
)

// TestDSL_LiteralDollarEscape locks the parser side of the `$$` escape: two
// consecutive dollars followed by a non-`$` produce exactly one
// KindLiteralDollar placeholder, while the sequence and error forms are
// UNCHANGED (the positive controls that stop the escape from swallowing
// `$$$X` or masking the `$$$$` error).
func TestDSL_LiteralDollarEscape(t *testing.T) {
	// `$${nm}` — the `$$` escape immediately followed by literal text.
	pat, err := Parse("$${nm}")
	if err != nil {
		t.Fatalf("Parse(\"$${nm}\") errored: %v", err)
	}
	if len(pat.Placeholders) != 1 {
		t.Fatalf("placeholder count = %d, want 1", len(pat.Placeholders))
	}
	ph := pat.Placeholders[0]
	if ph.Kind != KindLiteralDollar {
		t.Errorf("Kind = %v, want KindLiteralDollar", ph.Kind)
	}
	if ph.OffsetStart != 0 || ph.OffsetEnd != 2 {
		t.Errorf("offsets = [%d,%d), want [0,2)", ph.OffsetStart, ph.OffsetEnd)
	}
	if ph.Name != "" {
		t.Errorf("Name = %q, want empty (escape binds no capture)", ph.Name)
	}

	// A `$$` at end-of-string escapes cleanly (no error, one literal).
	endPat, err := Parse("a$$")
	if err != nil {
		t.Fatalf("Parse(\"a$$\") errored: %v", err)
	}
	if len(endPat.Placeholders) != 1 || endPat.Placeholders[0].Kind != KindLiteralDollar {
		t.Errorf("end-of-string $$ = %+v, want single KindLiteralDollar", endPat.Placeholders)
	}

	// POSITIVE CONTROL: three dollars is STILL a sequence capture, not two
	// literals — the escape must not extend its reach into `$$$X`.
	seqPat, err := Parse("$$$X")
	if err != nil {
		t.Fatalf("Parse(\"$$$X\") errored: %v", err)
	}
	if len(seqPat.Placeholders) != 1 || seqPat.Placeholders[0].Kind != KindSeq ||
		seqPat.Placeholders[0].Name != "X" {
		t.Errorf("$$$X = %+v, want single KindSeq named X", seqPat.Placeholders)
	}

	// POSITIVE CONTROL: `$$$$` is still the triple-dollar error.
	if _, err := Parse("$$$$"); err == nil {
		t.Error("Parse(\"$$$$\") = nil error; want errParserTripleDollar")
	} else if !strings.Contains(err.Error(), "$$$") {
		t.Errorf("Parse(\"$$$$\") err = %v; want triple-dollar error", err)
	}

	// POSITIVE CONTROL: a plain single-node capture is unchanged.
	nodePat, err := Parse("$X")
	if err != nil {
		t.Fatalf("Parse(\"$X\") errored: %v", err)
	}
	if len(nodePat.Placeholders) != 1 || nodePat.Placeholders[0].Kind != KindNode ||
		nodePat.Placeholders[0].Name != "X" {
		t.Errorf("$X = %+v, want single KindNode named X", nodePat.Placeholders)
	}
}

// TestSubstitute_LiteralDollar locks the engine side: a pattern mixing a `$N`
// capture with a `$$` escape substitutes the reserved identifier for N and a
// single literal `$`, and the returned substitution slice carries ONLY the
// capture (the literal contributes no substitution, so it never reaches the
// walker).
func TestSubstitute_LiteralDollar(t *testing.T) {
	pat := mustParse(t, "$N = $$")
	subst, subs := substitutePlaceholders(pat, goLangConfig)

	// The literal produces exactly one `$`; the capture produces a reserved
	// identifier that contains no `$`.
	if got := strings.Count(subst, "$"); got != 1 {
		t.Errorf("substituted %q has %d '$', want exactly 1 (the escape)", subst, got)
	}
	if !strings.Contains(subst, goLangConfig.Reserved) {
		t.Errorf("substituted %q missing reserved id for $N", subst)
	}

	// subs length equals the CAPTURE count — the literal is excluded.
	if len(subs) != 1 {
		t.Fatalf("subs length = %d, want 1 (capture count, literal excluded)", len(subs))
	}
	if subs[0].Placeholder.Kind != KindNode || subs[0].Placeholder.Name != "N" {
		t.Errorf("sub[0] = %+v, want KindNode named N", subs[0].Placeholder)
	}
}

// TestWalker_TemplateLiteralEscape is the end-to-end proof the escape delivers
// the capability: a TS template-literal pattern carrying `$$` MATCHES a real
// `${expr}` interpolation. The target carries a NON-matching interpolation
// line so a vacuous pass (0 or 2 matches) fails the assertion.
func TestWalker_TemplateLiteralEscape(t *testing.T) {
	pattern := "const $N = `hi $${nm} there`;"
	target := "const x = `hi ${nm} there`;\n" +
		"const y = `no ${other} thing`;\n" // negative control: different literal text

	matches := runTSWalker(t, pattern, target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1 (only the `hi ${nm} there` line; the `no ${other} thing` line must NOT match)", len(matches))
	}
	cap, ok := matches[0].captures["N"]
	if !ok {
		t.Fatalf("match missing N capture: %+v", matches[0].captures)
	}
	if cap.Text != "x" {
		t.Errorf("N = %q, want \"x\" (matched the wrong declaration)", cap.Text)
	}
}
