// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// legacy.go — what to say about a SERVER-CATALOG family that has no file entry.
//
// NOTHING EVER RESOLVES FROM THE CATALOG. The config file is the registration
// record; a catalog family with no entry is LEGACY, and it is surfaced rather
// than served: `knowledge collector list` names it with the entry it would need,
// and a collect on it FAILS naming the family, the file to write and the
// invocation that writes it. A fallback to the catalog would be a silently
// degraded path, which needs express approval recorded where it lives, and there
// is none.
//
// TWO CLASSES, BECAUSE THE ANSWER DIFFERS.
//
//   - CLASS A, a record written by the retired register tool: it carries a tool
//     and one provider, so the whole invocation is synthesizable — except the env
//     VALUES, which those records never held (their env was a list of NAMES the
//     daemon looked up), so those come out as placeholders the operator fills in.
//   - CLASS B, a pre-project exec-only record whose fields sat on proto numbers
//     now reserved: it decodes to a spec with no tool and no provider, so there
//     is no invocation to print. Such a record is ALREADY refused by the collect
//     path today, before this contract, for naming no tool — this class is named
//     here so nobody reads its refusal as something this contract caused.
const (
	// LegacyConvertible is the class-A label.
	LegacyConvertible = "LEGACY"
	// LegacyUnconvertible is the class-B label.
	LegacyUnconvertible = "LEGACY, UNCONVERTIBLE"
)

// EnvValuePlaceholder is what a synthesized invocation puts where a value would
// go. It is deliberately not an empty string: `-e NAME=` writes a variable set
// to the empty string, which is a DIFFERENT input from an unset one, so emitting
// it would hand the operator a command that silently registers the wrong thing.
const EnvValuePlaceholder = "<value>"

// LegacyClass reports the label for a catalog record with no file entry, and
// whether an invocation can be synthesized for it.
func LegacyClass(d *knowledgev1.GraphTypeDef) (label string, convertible bool) {
	col := d.GetCollector()
	if col.GetTool() == "" || (col.GetStdio() == nil && col.GetHttp() == nil) {
		return LegacyUnconvertible, false
	}
	return LegacyConvertible, true
}

// LegacyInvocation renders the `knowledge collector add` command that would
// write a class-A record as a file entry, or "" for class B.
func LegacyInvocation(d *knowledgev1.GraphTypeDef, scope Scope) string {
	if _, convertible := LegacyClass(d); !convertible {
		return ""
	}
	col := d.GetCollector()
	parts := []string{"knowledge", "collector", "add", "-s", string(scope)}
	switch {
	case col.GetStdio() != nil:
		s := col.GetStdio()
		parts = append(parts, "-t", TransportStdio, "--tool", shellToken(col.GetTool()))
		for _, name := range s.GetEnv() {
			parts = append(parts, "-e", shellToken(name+"="+EnvValuePlaceholder))
		}
		parts = append(parts, shellToken(d.GetName()), "--", shellToken(s.GetCommand()))
		for _, a := range s.GetArgs() {
			parts = append(parts, shellToken(a))
		}
	case col.GetHttp() != nil:
		parts = append(parts, "-t", TransportHTTP, "--tool", shellToken(col.GetTool()),
			shellToken(d.GetName()), shellToken(col.GetHttp().GetUrl()))
	}
	return strings.Join(parts, " ")
}

// shellToken quotes a token that would not survive being pasted into a shell as
// one word. A rendered command an operator cannot paste is a rendered command
// that teaches them the wrong invocation.
func shellToken(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'\\$") {
		return strconv.Quote(s)
	}
	return s
}
