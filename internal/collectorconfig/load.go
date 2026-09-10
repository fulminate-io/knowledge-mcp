// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/graphtypecrud"
)

// load.go — reading ONE scoped file: strict decode, duplicate-name refusal,
// expansion, then shape validation. Every failure names the file, the entry and
// the field, and nothing is skipped.
//
// AN ABSENT FILE IS AN EMPTY SCOPE; AN UNREADABLE ONE IS AN ERROR. That is the
// one place this package deliberately diverges from the repo manifest it is
// modeled on (tools/repo_manifest.go), which degrades a corrupt manifest to
// "unknown" because a corrupt manifest must not error a search. Here the file IS
// the registration record, so a file that exists and cannot be read is a
// registration whose contents are unknown — reporting "no collectors" for it
// would silently unregister every family the operator wrote down.
//
// EVERY READ RE-READS FROM DISK. There is no in-memory cache to go stale, which
// is the whole of the hand-edit requirement: an operator's edit takes effect at
// the next lookup with no daemon restart, exactly as repo_manifest.go states for
// its own file.

// loaded is one entry as the reader ended up with it: the EXPANDED entry, or —
// when one of its own `${VAR}` references resolves to nothing — the entry as
// written plus the refusal that belongs to IT.
//
// WHY THE REFUSAL RIDES THE ENTRY INSTEAD OF FAILING THE FILE. A reference the
// serving process cannot resolve is bad input about ONE entry: the variable the
// operator has to set is named in that entry and nowhere else. Returning it as
// the file's error made every OTHER collector in the same scope uncollectable —
// measured, with a credential-free sibling refused by a message naming a
// different entry — which is the entry's error widened into a scope-wide outage.
// Nothing is skipped, defaulted or degraded here: whoever asks for THIS entry
// gets THIS refusal, by name, and an entry nobody can use is never handed back
// as if it worked.
// THE ENTRY IS EMBEDDED so a reader of one field reads it off the row directly,
// which is what keeps this wrapper from rewriting every existing reader.
type loaded struct {
	// Entry is the expanded entry, or the entry exactly as written when
	// Unresolved is set — a half-expanded entry is a state no reader could
	// interpret, so the unexpanded text is what an operator is shown instead.
	Entry
	// Unresolved is this entry's own expansion failure, naming the file, the
	// entry, the field and the variable. nil for an entry that resolved.
	Unresolved error
}

// loadFile reads and validates one scoped file, returning its entries keyed by
// name. A file that does not exist yields an empty map and no error.
func loadFile(lookup varLookup, path string) (map[string]loaded, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the caller's own resolved config location.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]loaded{}, nil
		}
		return nil, fmt.Errorf("%s: the custom-collector config file cannot be read: %w", path, err)
	}
	return parseFile(lookup, path, raw)
}

// parseFile is loadFile's pure half: the bytes in, the EXPANDED and VALIDATED
// entries out. It is separate so every refusal class is provable without a
// filesystem.
// A STRUCTURAL REFUSAL IS THE FILE'S AND AN UNRESOLVED REFERENCE IS THE ENTRY'S.
// Bad JSON, an unknown key, a repeated name and a malformed shape describe a file
// nobody can read correctly, so they fail the load. A `${VAR}` this process
// cannot resolve describes ONE entry, and it rides that entry (see loaded).
func parseFile(lookup varLookup, path string, raw []byte) (map[string]loaded, error) {
	decoded, err := parseRaw(path, raw)
	if err != nil {
		return nil, err
	}
	out := make(map[string]loaded, len(decoded))
	// SORTED, so a file with two bad entries always reports the same one first.
	for _, name := range sortedEntryKeys(decoded) {
		expanded, err := expandEntry(lookup, path, name, decoded[name])
		if err != nil {
			// THE SHAPE IS NOT VALIDATED ON THIS ARM, deliberately: validation
			// describes the entry a collect would run, and the entry a collect
			// would run is the EXPANDED one, which this process could not build.
			// Judging the unexpanded text instead would refuse or admit a shape
			// that is not the one anybody uses.
			out[name] = loaded{Entry: decoded[name], Unresolved: err}
			continue
		}
		if err := validateEntry(path, name, expanded); err != nil {
			return nil, err
		}
		out[name] = loaded{Entry: expanded}
	}
	return out, nil
}

// parseRaw decodes the file's entries WITHOUT expanding or validating them.
//
// IT IS THE WRITE PATH'S READ. `knowledge collector add` rewrites the file
// whole, so it must read back exactly what the operator wrote: an entry loaded
// through the expanding path would have its `${TOKEN}` replaced by the token and
// the rewrite would bake a credential into the file. Structural refusals — bad
// JSON, an unknown key, a repeated name — still apply on both paths, because
// they describe a file nobody can read correctly either way.
func parseRaw(path string, raw []byte) (map[string]Entry, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		// An EMPTY FILE is an empty scope rather than invalid JSON: `collector
		// remove` of the last entry legitimately leaves one behind, and refusing it
		// would make removing an entry break every later lookup.
		return map[string]Entry{}, nil
	}

	// STEP 1 — the strict decode over the whole file. DisallowUnknownFields is
	// what refuses a key at ANY nesting level in one pass; the tool path needed one
	// hand-written check per level because it decodes a RawMessage per level.
	var top struct {
		Collectors map[string]json.RawMessage `json:"collectors"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&top); err != nil {
		return nil, fmt.Errorf("%s: the custom-collector config file does not decode: %w", path, err)
	}
	if err := refuseTrailingContent(dec); err != nil {
		return nil, fmt.Errorf("%s: the custom-collector config file does not decode: %w", path, err)
	}

	// STEP 2 — the duplicate-name pass. THE STRICT DECODE CANNOT SEE THIS: JSON
	// permits a repeated key and Go keeps the last one silently, so a file naming
	// one collector twice would register whichever was written last and say
	// nothing. This is a second walk of the same bytes, and it is not optional.
	if dup, err := duplicateEntryName(raw); err != nil {
		return nil, fmt.Errorf("%s: the custom-collector config file does not decode: %w", path, err)
	} else if dup != "" {
		return nil, fmt.Errorf("%s: collector %q: the file names this collector twice — JSON keeps the last of a repeated key silently, so one of the two would be registered and the other lost", path, dup)
	}

	// STEP 3 — per entry, in sorted order: decode strictly, which is what names
	// the entry an unknown key or a wrong-typed value sits in.
	out := make(map[string]Entry, len(top.Collectors))
	for _, name := range sortedRawKeys(top.Collectors) {
		e, err := decodeEntry(path, name, top.Collectors[name])
		if err != nil {
			return nil, err
		}
		out[name] = e
	}
	return out, nil
}

// refuseTrailingContent rejects a file carrying anything after the top-level
// object. json.Decoder.Decode stops at the end of the first value, so a file
// holding two objects would otherwise load the first and drop the second without
// a word.
func refuseTrailingContent(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing content after the top-level object")
	}
	return nil
}

// decodeEntry decodes one entry strictly, so an unknown key inside the entry,
// inside its behavior block or inside a node_types override is refused BY NAME
// rather than dropped.
func decodeEntry(path, name string, raw json.RawMessage) (Entry, error) {
	var e Entry
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return Entry{}, fmt.Errorf("%s: collector %q: %w", path, name, err)
	}
	return e, nil
}

// duplicateEntryName walks the raw bytes and returns the first collector name
// that appears twice inside the `collectors` object, or "" when none does.
func duplicateEntryName(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", errors.New("the top level is not an object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, _ := keyTok.(string)
		if key != "collectors" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return "", err
			}
			continue
		}
		return duplicateKeyInObject(dec)
	}
	return "", nil
}

// duplicateKeyInObject reads one object off dec, counting its keys.
func duplicateKeyInObject(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", errors.New("`collectors` is not an object")
	}
	seen := map[string]struct{}{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, _ := keyTok.(string)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return "", err
		}
		if _, dup := seen[key]; dup {
			return key, nil
		}
		seen[key] = struct{}{}
	}
	return "", nil
}

// validateEntry enforces the entry's shape on the EXPANDED entry, so a field
// that expanded to nothing is refused here rather than reaching a spawn.
//
// THE TRANSPORT NAMES ITSELF AND CARRIES ONLY ITS OWN FIELDS. A stdio entry
// carrying `url` or `headers`, or an http entry carrying `command`, `args` or
// `env`, is refused rather than half-honored: the operator who wrote it believed
// something would read it.
//
// THE FAMILY-NAME SHAPE RULE COMES FROM THE SHARED PREDICATE, not from a second
// copy here. A hand-edited file is the THIRD way a family name is admitted,
// beside `knowledge collector add` and the graph-type upsert, and it is the
// route an operator writing a collector by hand is most likely to take. A shape
// rule enforced on two of three admission routes is not enforced.
//
// IT IS THE SHAPE HALF ONLY, AND THAT IS DELIBERATE. graphtypecrud.ValidateName
// also carries the CLAIM rules — a built-in graph type's name, a retired one's —
// and this route does not answer that question. The collect dispatch
// short-circuits a built-in graph type before it ever reads a registration, and
// a client-side test proves that WITHOUT depending on any write path refusing
// the name, by writing exactly such an entry into a config file. Refusing it
// here would make that state unconstructible and quietly convert a client-side
// guarantee into a claim about this loader. The shape rule has no such tension:
// an identifier nobody can parse is wrong however the entry arrived.
func validateEntry(path, name string, e Entry) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s: an entry has an empty name — the entry name IS the graph family", path)
	}
	if err := graphtypecrud.ValidateNameSegments(name); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	switch strings.TrimSpace(e.Type) {
	case "":
		return fmt.Errorf("%s: collector %q: type is required — it is %q or %q", path, name, TransportStdio, TransportHTTP)
	case TransportStdio:
		if err := validateStdioEntry(path, name, e); err != nil {
			return err
		}
	case TransportHTTP:
		if err := validateHTTPEntry(path, name, e); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%s: collector %q: type %q is not a transport — it is %q or %q", path, name, e.Type, TransportStdio, TransportHTTP)
	}
	if strings.TrimSpace(e.Tool) == "" {
		return fmt.Errorf("%s: collector %q: tool is required — it names the single MCP tool the daemon calls on this provider to collect", path, name)
	}
	// THE CONTEXT DECLARATION IS CHECKED HERE, AT THE LOUDEST EARLY SITE. This
	// runs on every load and again inside `knowledge collector add` before the
	// entry is written, so a family this client cannot read is refused while the
	// operator is still looking at the command that produced it. The collect path
	// validates the same declaration again — a hand-edited file passes through no
	// write path — and both refusals come from the one validator, so the two
	// cannot drift into different answers.
	if err := e.Context.Validate(); err != nil {
		return fmt.Errorf("%s: collector %q: %w", path, name, err)
	}
	return validateNames(path, name, e)
}

// validateStdioEntry enforces the stdio transport's own fields.
func validateStdioEntry(path, name string, e Entry) error {
	if strings.TrimSpace(e.Command) == "" {
		return fmt.Errorf("%s: collector %q: command is required for a %q entry", path, name, TransportStdio)
	}
	if e.URL != "" {
		return fmt.Errorf("%s: collector %q: url belongs to a %q entry, and this entry is %q", path, name, TransportHTTP, TransportStdio)
	}
	if e.Headers != nil {
		return fmt.Errorf("%s: collector %q: headers belong to a %q entry, and this entry is %q", path, name, TransportHTTP, TransportStdio)
	}
	return nil
}

// validateHTTPEntry enforces the http transport's own fields, including the url
// shape the transport would otherwise discover at dial time.
func validateHTTPEntry(path, name string, e Entry) error {
	raw := strings.TrimSpace(e.URL)
	if raw == "" {
		return fmt.Errorf("%s: collector %q: url is required for a %q entry", path, name, TransportHTTP)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: collector %q: url %q does not parse: %w", path, name, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: collector %q: url %q must be an http or https URL", path, name, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: collector %q: url %q names no host", path, name, raw)
	}
	if e.Command != "" {
		return fmt.Errorf("%s: collector %q: command belongs to a %q entry, and this entry is %q", path, name, TransportStdio, TransportHTTP)
	}
	if e.Args != nil {
		return fmt.Errorf("%s: collector %q: args belong to a %q entry, and this entry is %q", path, name, TransportStdio, TransportHTTP)
	}
	if e.Env != nil {
		return fmt.Errorf("%s: collector %q: env belongs to a %q entry, and this entry is %q", path, name, TransportStdio, TransportHTTP)
	}
	return nil
}

// validateNames refuses an empty env or header KEY and an empty node-type key.
// An empty key is not a name, and the block it sits in is the one place a reader
// looks to see what an entry supplies.
func validateNames(path, name string, e Entry) error {
	for _, k := range sortedKeys(e.Env) {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("%s: collector %q: env has an empty variable name", path, name)
		}
		if strings.Contains(k, "=") {
			return fmt.Errorf("%s: collector %q: env name %q contains %q — the KEY is the variable name and the VALUE is its value", path, name, k, "=")
		}
	}
	for _, k := range sortedKeys(e.Headers) {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("%s: collector %q: headers has an empty header name", path, name)
		}
	}
	for nt := range e.NodeTypes {
		if strings.TrimSpace(nt) == "" {
			return fmt.Errorf("%s: collector %q: node_types has an empty node-type key", path, name)
		}
	}
	if err := validateDeclaredVocabulary(path, name, e); err != nil {
		return err
	}
	if err := validateEnvDeclaration(path, name, e); err != nil {
		return err
	}
	return validateFieldLists(path, name, e)
}

// sortedNodeTypeKeys walks a node-type override map deterministically, so a file
// with two bad overrides is refused with the same message on every run — the same
// property sortedKeys gives the env and header walks.
func sortedNodeTypeKeys(m map[string]NodeTypeOverride) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedKeys returns a map's keys in sorted order, so every walk over an entry's
// blocks is deterministic — in a refusal message, in the child's environment and
// in the collector identity alike.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedRawKeys is sortedKeys for the undecoded entry map.
func sortedRawKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedEntryKeys is sortedKeys for the decoded entry map.
func sortedEntryKeys(m map[string]Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
