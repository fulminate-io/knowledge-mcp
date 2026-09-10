// SPDX-License-Identifier: Apache-2.0

package collectorconfig

import (
	"maps"
	"slices"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// declaration.go — FILLING AN ENTRY FROM WHAT A COLLECTOR DECLARED.
//
// `knowledge collector add` dials the provider, calls its describe tool once and
// arrives here with two things: the entry as the operator typed it, and the
// collector's own declaration. This file is the merge, and the merge has exactly
// one rule that matters:
//
//	THE OPERATOR'S FLAGS WIN, AND THE TWO LLM AXES ARE THE OPERATOR'S ALONE.
//
// Summarizing and embedding are spend on the operator's account. A collector may
// SUGGEST them — the suggestion is printed by the add — and no path here writes
// them into the entry; a declaration that could set them would make installing a
// collector a way to opt an operator into a bill. Everything else a collector
// declares is a fact about the collector rather than a preference about the
// operator's money, so it is written as declared unless a flag said otherwise.
//
// AN ENV VALUE IS NEVER TAKEN FROM THE DECLARATION, because a declaration
// carries no values — only names and their class. The values come from the
// process running the add, exactly as they do for the installer, under the same
// three-class policy: a path or selector literal when it is set, and a secret in
// no state whatsoever.

// EnvLookup reads one variable from the environment the add is running in. It is
// a parameter rather than a direct os.LookupEnv call so a test drives the whole
// three-class policy without touching the process environment.
type EnvLookup func(name string) (string, bool)

// ApplyDeclaration returns the entry with every declared fact filled in, and the
// names of the SECRET-class variables it deliberately did not write.
//
// The skipped names are returned rather than logged because the operator is owed
// them: someone who exported a credential and finds it absent from the entry
// needs to know that the absence was a decision. Names only, never values.
func ApplyDeclaration(e Entry, decl *externalcollector.Declaration, env EnvLookup) (Entry, []string) {
	if decl == nil {
		return e, nil
	}
	e.Vocabulary = &Vocabulary{
		NodeTypes: nonNil(decl.NodeTypes),
		EdgeTypes: nonNil(decl.EdgeTypes),
	}
	if len(decl.Environment) > 0 {
		e.EnvDeclaration = slices.Clone(decl.Environment)
	}
	if len(e.Context) == 0 && len(decl.Context) > 0 {
		e.Context = decl.Context
	}
	if len(e.NodeTypes) == 0 && len(decl.NodeTypeOverrides) > 0 {
		e.NodeTypes = nodeTypeOverridesFromDeclaration(decl.NodeTypeOverrides)
	}
	e.Behavior = mergedBehavior(e.Behavior, decl.Behavior)
	// THE ENV BLOCK IS THE STDIO CHILD'S WHOLE ENVIRONMENT AND EXISTS ON NO OTHER
	// TRANSPORT. An http provider runs in a process this client did not spawn and
	// whose environment it cannot set, and the loader refuses an http entry
	// carrying `env` by name — so deriving one here would write a file the next
	// load rejects. The DECLARATION still rides an http entry: what a remote
	// collector reads is true about it, and an installer's table derives from the
	// declaration rather than from the block.
	if e.Type != TransportStdio {
		return e, nil
	}
	envBlock, skipped := declaredEnv(e.Env, decl.Environment, env)
	e.Env = envBlock
	return e, skipped
}

// nonNil renders a declared list as a NON-NIL slice, so a collector declaring an
// empty vocabulary reaches the file as `[]` rather than as `null`. The two mean
// different things one layer down — declared-empty refuses every type, absent
// accepts every type — and the describe schema requires both lists, so a
// declaration that reached here at all declared both.
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return slices.Clone(in)
}

// nodeTypeOverridesFromDeclaration converts the declared per-node-type overrides
// into the entry's own shape.
func nodeTypeOverridesFromDeclaration(in map[string]externalcollector.DeclaredNodeTypeOverride) map[string]NodeTypeOverride {
	out := make(map[string]NodeTypeOverride, len(in))
	for nt, ov := range in {
		out[nt] = NodeTypeOverride{
			Summarizable:    ov.Summarizable,
			Embeddable:      ov.Embeddable,
			EmbedFields:     slices.Clone(ov.EmbedFields),
			SummarizeFields: slices.Clone(ov.SummarizeFields),
			Bm25Fields:      slices.Clone(ov.Bm25Fields),
		}
	}
	return out
}

// mergedBehavior composes the written behavior block from the operator's flags
// and the collector's declaration.
//
// WHAT THE DECLARATION MAY CONTRIBUTE: the three field lists, which are facts
// about the collector's own nodes that nobody else knows; and syncable, but ONLY
// when it is declared false. A declared `syncable: true` is written nowhere,
// because true is already the loader's default and writing it would put a
// behavior block on every entry — turning "the operator said nothing about
// spend" into "there is a behavior block here" for whoever reads the file next.
// A declared FALSE is a real departure and is recorded.
//
// WHAT IT MAY NEVER CONTRIBUTE: summarizable and embeddable. They come from the
// operator's flags or they are absent, so an add that mentioned neither writes
// no such key at all and the file records that the operator said nothing.
func mergedBehavior(fromFlags *Behavior, declared *externalcollector.DeclaredBehavior) *Behavior {
	if declared == nil {
		return fromFlags
	}
	out := Behavior{}
	if fromFlags != nil {
		out = *fromFlags
	}
	if out.EmbedFields == nil {
		out.EmbedFields = slices.Clone(declared.EmbedFields)
	}
	if out.SummarizeFields == nil {
		out.SummarizeFields = slices.Clone(declared.SummarizeFields)
	}
	if out.Bm25Fields == nil {
		out.Bm25Fields = slices.Clone(declared.Bm25Fields)
	}
	if out.Syncable == nil && declared.Syncable != nil && !*declared.Syncable {
		out.Syncable = new(false)
	}
	if out.Summarizable == nil && out.Embeddable == nil && out.Syncable == nil &&
		out.EmbedFields == nil && out.SummarizeFields == nil && out.Bm25Fields == nil && out.Extra == nil {
		return nil
	}
	return &out
}

// declaredEnv composes the entry's env block from the declared names and the
// environment this process is running in, and returns the secret-class names it
// refused to write.
//
// THE FOUR CLASSES, and the class decides what is WRITTEN rather than only what
// the variable means:
//
//   - path: the literal when it is set. Without it a provider's file-based
//     credential chain resolves nothing inside a child whose environment is
//     exactly this block.
//   - selector: the literal when it is set; the key is OMITTED ENTIRELY when it
//     is unset, because an empty selector selects the thing named by "" and
//     resolves nothing.
//   - secret: NOTHING, in any state. Not the value, and not a `${NAME}`
//     reference either: a reference is expanded by the process SERVING the
//     collect, whose environment under a service manager holds almost nothing,
//     so it reaches the collector present-and-empty — and collectors that tell
//     present-and-empty apart from absent then fail on a credential that looks
//     like it was supplied.
//   - not-carried: NOTHING either, and for a different reason: the collector
//     READS the name and an installed entry deliberately does not declare it —
//     a machine fact, a platform the installer does not target, or a non-default
//     deployment an operator configures by hand. It is also the one class whose
//     name is NOT reported as withheld, because a decision the collector's
//     author already made is not news an operator acts on.
//
// AN OPERATOR'S OWN `-e` FLAG WINS over anything derived here, including for a
// secret-class name: someone who typed the key knows what it costs, and this
// path exists to keep them from having to type the other twenty.
func declaredEnv(fromFlags map[string]string, declared []externalcollector.DeclaredEnv, env EnvLookup) (map[string]string, []string) {
	if env == nil || len(declared) == 0 {
		return fromFlags, nil
	}
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the declared names cost at most a growth — while a size built by
	// addition is a value the allocator has to take on trust.
	out := make(map[string]string, len(fromFlags))
	maps.Copy(out, fromFlags)
	var skipped []string
	for _, e := range declared {
		if _, given := out[e.Name]; given {
			continue // the operator wrote this one themselves.
		}
		value, set := env(e.Name)
		switch e.Class {
		case externalcollector.EnvClassSecret:
			if set {
				skipped = append(skipped, e.Name)
			}
		case externalcollector.EnvClassPath, externalcollector.EnvClassSelector:
			if set && value != "" {
				out[e.Name] = value
			}
		case externalcollector.EnvClassNotCarried:
			// NOTHING, and NOT reported as skipped. A secret withheld is news an
			// operator acts on; a name the collector's author decided no installed
			// entry should declare is not, and reporting it on every add would
			// bury the credential line under a list nobody reads.
		}
	}
	// SORTED BEFORE EITHER RETURN, so the skipped-name report an operator reads is
	// the same on every run rather than in the declaration's order on one path and
	// unsorted on the other.
	slices.Sort(skipped)
	if len(out) == 0 {
		// NIL RATHER THAN AN EMPTY MAP, so an entry that ends up with no
		// environment carries no `env` key at all — the same property the flag
		// renderers hold, and what keeps a second add byte-identical.
		return nil, skipped
	}
	return out, skipped
}
