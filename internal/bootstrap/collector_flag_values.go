// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// collector_flag_values.go holds the three flag.Value implementations the
// collector verbs register: the repeatable key/value flag, the tri-state boolean,
// and the repeatable field list. Split out of collector_subcommand.go when the
// behavior-block options landed and that file reached the per-file size budget;
// the verbs, the parse and the handlers stay there.
//
// ALL THREE EXIST TO PRESERVE ABSENCE. Go's flag package renders an option
// nobody gave as its zero value, and a zero value written into the entry is
// indistinguishable from a declaration. Each type below keeps "not given" as its
// own state and renders it as nil, so an absent option leaves its key out of the
// written file entirely — which is what a later reader, and the loader's own
// defaults, both read as "nobody said".

// keyValueFlag is the repeatable `-e KEY=VALUE` / `-H 'Name: value'` flag,
// following the in-tree repeatable-flag idiom (a slice type with a Set method
// that appends).
//
// IT SPLITS ON THE FIRST SEPARATOR ONLY. A value legitimately contains the
// separator — a URL carries `=` in its query string, a header value carries `:`
// after a scheme — and splitting on the last, or refusing more than one, would
// mangle exactly the values an operator most needs to pass.
type keyValueFlag struct {
	sep   string
	label string
	pairs []kvPair
}

type kvPair struct{ key, value string }

func (f *keyValueFlag) String() string { return "" }

func (f *keyValueFlag) Set(raw string) error {
	key, value, ok := strings.Cut(raw, f.sep)
	if !ok {
		return fmt.Errorf("%s %q is not %s-separated (expected %s)", f.label, raw, f.sep, f.label)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("%s %q has an empty name", f.label, raw)
	}
	f.pairs = append(f.pairs, kvPair{key: key, value: strings.TrimSpace(value)})
	return nil
}

// asMap renders the pairs as a map, or nil when none was supplied. Nil rather
// than an empty map is what keeps an absent block absent in the written file.
func (f *keyValueFlag) asMap() map[string]string {
	if len(f.pairs) == 0 {
		return nil
	}
	out := make(map[string]string, len(f.pairs))
	for _, p := range f.pairs {
		out[p.key] = p.value
	}
	return out
}

// triBoolFlag is a boolean flag that reports whether it was SET, which a plain
// flag.Bool cannot.
//
// THE THIRD STATE IS THE ONE THAT MATTERS. The entry's behavior booleans are
// POINTERS precisely so an omitted key stays distinguishable from an explicit
// false, and the opt-in default makes that distinction load-bearing: both reach
// the same server behavior, but "nobody said" and "this family opted out" read
// completely differently to whoever inherits the file. A flag.Bool would collapse
// them, writing an explicit false for every add that never mentioned the option.
//
// IT REQUIRES THE `=` FORM for an explicit value (`--embeddable=false`), which is
// what Go's flag package does for any boolean; the bare `--embeddable` spelling
// means true, as it does everywhere else.
type triBoolFlag struct {
	set bool
	val bool
}

func (f *triBoolFlag) String() string { return "" }

// IsBoolFlag tells the flag package this option takes no separate argument, so
// `--embeddable name` parses the name as a positional rather than as the value.
func (f *triBoolFlag) IsBoolFlag() bool { return true }

func (f *triBoolFlag) Set(raw string) error {
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("%q is not a boolean — write true or false", raw)
	}
	f.set, f.val = true, v
	return nil
}

// ptr renders the flag as the entry's optional bool: nil when the flag was never
// given, so an absent option leaves the key out of the written file.
func (f *triBoolFlag) ptr() *bool {
	if !f.set {
		return nil
	}
	v := f.val
	return &v
}

// listFlag is the repeatable field-list flag, following the same in-tree
// repeatable-flag idiom keyValueFlag uses: a Set that appends, and a renderer
// that returns NIL rather than an empty slice when nothing was supplied.
//
// NIL RATHER THAN EMPTY IS THE WHOLE PROPERTY, and it is what keeps an absent
// block absent in the written file. It also keeps this command from writing the
// one shape the loader refuses: a declared-but-empty list is an operator mistake,
// and a flag that rendered `[]` for an option nobody gave would manufacture that
// mistake on every add.
//
// IT DOES NOT SPLIT ON COMMAS. A field name is a node field or a metadata key,
// and a metadata key is operator-chosen text that may legitimately contain a
// comma; repeating the flag is unambiguous where a delimiter is a guess.
type listFlag struct {
	label  string
	values []string
}

func (f *listFlag) String() string { return "" }

func (f *listFlag) Set(raw string) error {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fmt.Errorf("%s was given an empty field name", f.label)
	}
	f.values = append(f.values, v)
	return nil
}

func (f *listFlag) asList() []string {
	if len(f.values) == 0 {
		return nil
	}
	return append([]string(nil), f.values...)
}

// registerBehaviorFlags registers the five behavior-block options: the two LLM
// axis booleans and the three field lists.
//
// THE TWO BOOLEANS ARE OPT-IN AND THE HELP TEXT SAYS SO, because the surprising
// half is the default rather than the flag. Summarizing and embedding are LLM
// spend, so an operator gets them by asking; leaving both out still yields a
// graph that is text-searchable, which is what most collectors want.
//
// EACH OPTION REGISTERS TWO SPELLINGS AGAINST ONE VARIABLE, matching every other
// option in this flag set. The short list spellings are two letters (-ef, -sf,
// -bf) rather than one, because the single letters that would fit are already
// taken by the transport and the env flags.
func registerBehaviorFlags(fs *flag.FlagSet, f *collectorFlags) {
	f.embedFields = listFlag{label: "--embed-fields"}
	f.summarizeFields = listFlag{label: "--summarize-fields"}
	f.bm25Fields = listFlag{label: "--bm25-fields"}

	const summarizeHelp = "send this family's nodes to the LLM summarizer (default off; " +
		"a node the collector already summarized is never re-summarized)"
	const embedHelp = "embed this family's nodes for semantic search (default off; " +
		"the graph is keyword-searchable either way)"
	fs.Var(&f.summarizable, "summarizable", summarizeHelp)
	fs.Var(&f.summarizable, "summarize", summarizeHelp)
	fs.Var(&f.embeddable, "embeddable", embedHelp)
	fs.Var(&f.embeddable, "embed", embedHelp)

	const embedFieldsHelp = "node field or metadata key the embed text is composed from, repeatable " +
		"(default: symbol_name, summary, keywords, description, content)"
	const summarizeFieldsHelp = "node field or metadata key the summarizer input is composed from, repeatable " +
		"(same default as --embed-fields)"
	const bm25FieldsHelp = "node field or metadata key the keyword document is composed from, repeatable " +
		"(same default as --embed-fields)"
	fs.Var(&f.embedFields, "embed-fields", embedFieldsHelp)
	fs.Var(&f.embedFields, "ef", embedFieldsHelp)
	fs.Var(&f.summarizeFields, "summarize-fields", summarizeFieldsHelp)
	fs.Var(&f.summarizeFields, "sf", summarizeFieldsHelp)
	fs.Var(&f.bm25Fields, "bm25-fields", bm25FieldsHelp)
	fs.Var(&f.bm25Fields, "bf", bm25FieldsHelp)
}

// behaviorFromFlags renders the five options as the entry's behavior block, or
// NIL when the add mentioned none of them.
//
// NIL FOR "NONE GIVEN" IS THE PROPERTY THE WHOLE BLOCK RESTS ON. An `add` with no
// behavior options must write no `behavior` key at all, so the file records that
// the operator said nothing — which the loader reads as its defaults and a later
// reader reads as an open question. A block written with every field at its zero
// value would say something quite different and would be indistinguishable from a
// deliberate opt-out.
func behaviorFromFlags(f collectorFlags) *collectorconfig.Behavior {
	b := collectorconfig.Behavior{
		Summarizable:    f.summarizable.ptr(),
		Embeddable:      f.embeddable.ptr(),
		EmbedFields:     f.embedFields.asList(),
		SummarizeFields: f.summarizeFields.asList(),
		Bm25Fields:      f.bm25Fields.asList(),
	}
	if b.Summarizable == nil && b.Embeddable == nil &&
		b.EmbedFields == nil && b.SummarizeFields == nil && b.Bm25Fields == nil {
		return nil
	}
	return &b
}
