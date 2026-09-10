// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// load_declared.go — the loader's refusals for the DECLARED blocks of an entry:
// the three field lists, the closed type vocabulary and the environment
// declaration. Split out of load.go when that file reached this repository's
// per-file size budget; the file walk, the strict decode and the transport
// refusals stay there.
//
// EVERY REFUSAL HERE IS ABOUT A BLOCK THE OPERATOR OR THE COLLECTOR DECLARED,
// and each one distinguishes an ABSENT block from an EMPTY one, because the two
// mean different things on every one of these three: an absent field list takes
// the server's default and an empty one says nothing at all; an absent
// vocabulary is accept-all and an empty one refuses every type; an absent
// environment declaration is a collector that named none.

// validateFieldLists refuses a DECLARED field list that is empty, that carries a
// blank entry, or that names the same field twice — on all three lists and at
// both cascade levels.
//
// AN ABSENT LIST IS NOT AN EMPTY ONE, and keeping the two apart is the whole
// point of refusing here. The server falls back to a default field shape for an
// axis that is on and declares NO list, so "the operator named nothing" is a
// meaningful state with a defined outcome. `"embed_fields": []` is a different
// thing: an operator who typed it meant to say something and said nothing, and
// silently treating it as "nobody declared" would compose text they did not ask
// for. Absent is inherit-or-default; empty is a mistake.
//
// A BLANK ENTRY AND A DUPLICATE ARE THE SAME KIND OF MISTAKE ONE LEVEL DOWN. A
// blank name resolves to nothing and contributes nothing, so it is a line the
// operator believed was doing work; a duplicate composes the same text twice into
// the embed input, which is a worse document rather than an error the composer
// could report.
//
// IT REFUSES SHAPES, NOT NAMES. An unfamiliar field name is admitted: the server
// resolves an unrecognized name as a metadata key, which is a supported way to
// compose from data this config format knows nothing about. Refusing names here
// would invent a closed vocabulary the format does not have, and would break
// every entry composing from its own metadata.
//
// IT IS A LOADER REFUSAL AND NOT A COMPOSER ONE. The composer's tolerance for a
// name that resolves to nothing on a given node is a separate, pinned contract,
// and the resolver's empty-means-inherit cascade is untouched: this runs before
// the list is ever persisted, so the two live at different layers and neither
// moves for the other.
func validateFieldLists(path, name string, e Entry) error {
	if e.Behavior != nil {
		for _, l := range []struct {
			field string
			list  []string
		}{
			{"embed_fields", e.Behavior.EmbedFields},
			{"summarize_fields", e.Behavior.SummarizeFields},
			{"bm25_fields", e.Behavior.Bm25Fields},
		} {
			if err := validateFieldList(path, name, "", l.field, l.list); err != nil {
				return err
			}
		}
	}
	for _, nt := range sortedNodeTypeKeys(e.NodeTypes) {
		ov := e.NodeTypes[nt]
		for _, l := range []struct {
			field string
			list  []string
		}{
			{"embed_fields", ov.EmbedFields},
			{"summarize_fields", ov.SummarizeFields},
			{"bm25_fields", ov.Bm25Fields},
		} {
			if err := validateFieldList(path, name, nt, l.field, l.list); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateFieldList checks ONE list. nodeType is "" for the graph level and the
// node-type key otherwise, so the refusal says which of the two cascade levels
// the operator has to go and fix.
func validateFieldList(path, name, nodeType, field string, list []string) error {
	where := fmt.Sprintf("%s: collector %q: %s", path, name, field)
	if nodeType != "" {
		where = fmt.Sprintf("%s: collector %q: node_types %q: %s", path, name, nodeType, field)
	}
	if list == nil {
		return nil // absent: inherit the level above, or take the server's default.
	}
	if len(list) == 0 {
		return fmt.Errorf("%s is declared empty — name at least one field, or omit the key "+
			"entirely to take the default field shape", where)
	}
	seen := make(map[string]struct{}, len(list))
	for _, f := range list {
		if strings.TrimSpace(f) == "" {
			return fmt.Errorf("%s has a blank field name — every entry names a node field or a "+
				"metadata key", where)
		}
		if _, dup := seen[f]; dup {
			return fmt.Errorf("%s names %q twice — a duplicate composes the same text twice into "+
				"the same document", where, f)
		}
		seen[f] = struct{}{}
	}
	return nil
}

// validateDeclaredVocabulary refuses a type vocabulary that names nothing or
// names the same type twice.
//
// AN EMPTY VOCABULARY IS LEGAL HERE AND AN EMPTY FIELD LIST IS NOT, which looks
// inconsistent until you read what each one means downstream. An empty field
// list has a defined fallback — the server composes from its default shape — so
// declaring one says nothing and is a mistake. An empty VOCABULARY has no
// fallback: it is the collector declaring that it emits no type at all, and the
// server refuses every node it then sends. That is a real statement, and the
// absence of the key is the other one (accept-all, for a family registered
// before the describe tool existed). Both are meaningful, so neither is refused.
func validateDeclaredVocabulary(path, name string, e Entry) error {
	if e.Vocabulary == nil {
		return nil
	}
	for _, pair := range []struct {
		field string
		types []string
	}{
		{"node_types", e.Vocabulary.NodeTypes},
		{"edge_types", e.Vocabulary.EdgeTypes},
	} {
		seen := make(map[string]struct{}, len(pair.types))
		for _, t := range pair.types {
			if strings.TrimSpace(t) == "" {
				return fmt.Errorf("%s: collector %q: vocabulary %s has a blank type name — a vocabulary entry names a type",
					path, name, pair.field)
			}
			if _, dup := seen[t]; dup {
				return fmt.Errorf("%s: collector %q: vocabulary %s names %q twice", path, name, pair.field, t)
			}
			seen[t] = struct{}{}
		}
	}
	return nil
}

// validateEnvDeclaration refuses an environment declaration an installer could
// not act on: a name that is not a variable name, a class outside the closed
// three, or one name declared twice with two classes.
//
// IT IS CHECKED ON EVERY LOAD AND NOT ONLY AT THE ADD, because a hand-edited
// file passes through no write path — and the class is what decides whether a
// value is written into a config file at all.
func validateEnvDeclaration(path, name string, e Entry) error {
	seen := make(map[string]struct{}, len(e.EnvDeclaration))
	for i, decl := range e.EnvDeclaration {
		if err := externalcollector.ValidateDeclaredEnv(decl); err != nil {
			return fmt.Errorf("%s: collector %q: env_declaration[%d]: %w", path, name, i, err)
		}
		if _, dup := seen[decl.Name]; dup {
			return fmt.Errorf("%s: collector %q: env_declaration names %q twice; one name has one class", path, name, decl.Name)
		}
		seen[decl.Name] = struct{}{}
	}
	return nil
}
