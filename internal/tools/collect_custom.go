// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_custom.go — the CUSTOM (registered, non-builtin) collect: resolving
// the registration record, and the runner that proxies its MCP provider.
//
// Everything else a custom collect does is the builtin path's code, reached by
// handing this runner to collectWork. The split is deliberately this small: the
// only thing a custom collect does differently is WHERE THE NODES COME FROM.

// collectRunner produces the graph for one collect. It is the seam that lets a
// custom collect ride the builtin tail: collectWork calls it exactly where the
// builtin path called collector.Collect, and everything after — the linker, the
// postpopulate hook, the pipeline wake, the composition verdict — is shared.
// The SECOND RETURN is the foreign-context fill report, rendered. It is a plain
// string rather than a structured value because the only consumer is the collect
// result text, and it is on the RUNNER rather than on CollectComposition because
// a composition is what a HARVEST produced and this is what the harvest was
// GIVEN. The builtin runner returns the empty string, which the suffixing
// contract degrades to the result text unchanged.
type collectRunner func(ctx context.Context, a collectArgs, opts collector.CollectOptions) (collector.CollectComposition, string, error)

// builtinCollectRunner is the builtin registry's collect: look the collector up
// by type, walk, census, ship.
func builtinCollectRunner() collectRunner {
	return func(ctx context.Context, a collectArgs, opts collector.CollectOptions) (collector.CollectComposition, string, error) {
		// NO FOREIGN CONTEXT REACHES A BUILTIN COLLECT. The declaration lives on a
		// registration entry and a builtin type can hold none, so there is nothing
		// to report and the empty string is the honest answer rather than a
		// placeholder.
		comp, err := collector.Collect(ctx, a.Type, a.ID, opts)
		return comp, "", err
	}
}

// customCollectRunner proxies the registration's MCP provider and ships the
// result through the same sink a builtin collect writes to.
//
// It mirrors collector.Collect step for step — produce, census, ship — because
// the census and the ship are what the tail downstream reads. What it does NOT
// do is look the type up in the builtin registry: a custom type is not in it,
// which is the whole reason this runner exists.
//
// THE PROVIDER RETURNS TWO HALVES AND THIS IS WHERE THEY PART. The CollectResult
// is the collect's own graph and goes to the sink; the cross-graph edges are
// resolved and linked into the linkage graph by the pass BETWEEN the two calls
// below. The ordering is the requirement, not a preference: an edge that reached
// Sink.WriteResult would already be an edge in the collector's own graph, and
// the tail past this runner receives a CollectComposition, which retains counts
// and not edges, so nothing downstream could take it back.
//
// THE DECLARED CONTEXT IS FILLED FIRST, BEFORE THE PROVIDER CALL, because this
// is the innermost frame that holds both of the fill's inputs: the registration
// (which carries the declaration) and the client deps (which carry the two graph
// read seams). A fill failure refuses the collect with the same wrap every other
// provider-side failure gets, naming the file: a module that declared context
// and was called without it would compute from nothing and report success.
//
// THE TWO PASSES CANNOT CONTEND FOR A POSITION, and that is a property of what
// each consumes rather than an ordering anyone chose. The fill produces one of
// the provider call's ARGUMENTS, so it runs before it; the cross-graph
// resolution consumes the call's RETURN, so it runs after. Read top to bottom:
// fill, call, resolve, ship.
//
// IT TAKES deps FOR BOTH. collector.CollectOptions carries no graph caller and
// is shared with the builtin runner, so widening it would hand every builtin
// collector a capability only this path needs; deps is in scope at the
// construction site and rides the closure instead.
func customCollectRunner(deps ClientDeps, reg *resolvedCollector) collectRunner {
	return func(ctx context.Context, a collectArgs, opts collector.CollectOptions) (collector.CollectComposition, string, error) {
		foreign, fillReport, err := fillCollectContext(ctx, deps, reg.Context)
		if err != nil {
			return collector.CollectComposition{}, "", fmt.Errorf("collect %s: %s: collector %q: %w", a.Type, reg.Path, a.Type, err)
		}
		// THE REPORT IS RENDERED BEFORE THE PROVIDER CALL, because what it reports is
		// what this collect SUPPLIED and a provider failure does not make the fill's
		// counts untrue. It is returned on every path so the value is available to a
		// caller that wants it; today the only caller renders it on the SUCCESS path
		// alone (collect_detach.go returns the runner's error verbatim), so a failed
		// collect does not show the counts.
		fill := fillReport.Render()
		res, crossGraph, err := externalcollector.RunMCP(ctx, reg.Registration, a.Params, a.ID, foreign)
		if err != nil {
			// THE FILE IS NAMED, NOT JUST THE FAMILY. Precedence is project then
			// user and the winning entry is taken whole, so the same family name
			// legitimately exists in two files; a refusal naming only the family
			// leaves an operator re-running `knowledge collector list` to find out
			// which entry was dialed. The provider contract check runs here on every
			// collect, including the first collect of a hand-edited entry, and that
			// is the failure this wrap is mostly carrying.
			return collector.CollectComposition{}, fill, fmt.Errorf("collect %s: %s: collector %q: %w", a.Type, reg.Path, a.Type, err)
		}
		if opts.Sink == nil {
			return collector.CollectComposition{}, fill, fmt.Errorf("collect %s: no Sink to ship the collected graph through", a.Type)
		}
		if cerr := resolveCrossGraphEdges(ctx, deps, a.ID, crossGraph); cerr != nil {
			return collector.CollectComposition{}, fill, cerr
		}
		comp := collector.NewCollectComposition(res)
		// THE UNDECLARED-VOCABULARY NOTICE, composed ONCE PER COLLECT and here
		// rather than per chunk: a collect issues one chunk per 4 MiB of nodes and
		// another per 4 MiB of edges, and a per-chunk notice would repeat itself
		// however many times the payload happened to divide into.
		//
		// IT IS THE CLIENT'S TO EMIT because the client is the side that KNOWS. It
		// resolves the entry on every collect and can see whether a vocabulary was
		// declared; the server sees only an absent field on a record and cannot
		// tell an old registration from a new one. That is also why this needs no
		// response-message change.
		comp.Notices = append(comp.Notices, undeclaredVocabularyNotice(a.Type, reg)...)
		if err := opts.Sink.WriteResult(ctx, a.Type, res); err != nil {
			return collector.CollectComposition{}, fill, err
		}
		return comp, fill, nil
	}
}

// undeclaredVocabularyNotice returns the one-line notice a family whose
// registration declares NO type vocabulary owes its operator, or nothing at all.
//
// WHY THIS IS SAID OUT LOUD. The collector contract requires a describe tool,
// and every entry written by `knowledge collector add` since carries the node
// and edge types its collector declared; the server refuses anything outside
// them. A record written BEFORE that keeps accept-all, because the persisted
// format owes one-version-back load — so this one family is collected under a
// rule none of the operator's others follow. That is a silent widening unless
// something says it, and the client is the only side that can: the server sees
// an absent field and cannot tell an old registration from a new one.
//
// IT NAMES THE FILE, not just the family, because the same family name
// legitimately exists in both scopes and the operator's next move is to re-run
// the add against the entry that produced this.
func undeclaredVocabularyNotice(family string, reg *resolvedCollector) []string {
	if reg == nil || reg.Registration == nil || reg.Def.GetVocabulary() != nil {
		return nil
	}
	return []string{fmt.Sprintf(
		"the collector registered for %q declares NO node or edge type vocabulary (%s), so every type it emits is accepted; "+
			"re-run `knowledge collector add` for it to record what it emits and have undeclared types refused",
		family, reg.Path)}
}

// lookupCustomCollector resolves the registration for a collect type FROM THE
// CONFIG FILES, which are the registration record. It returns (nil, nil) for a
// BUILTIN GRAPH TYPE and for a type no file entry names — the caller then runs
// the builtin path, whose own unknown-type error names the type.
//
// THE FILE ENTRY WINS, AND THE COMPILED-IN COLLECTOR IS THE FALLBACK. The files
// are consulted FIRST, so a collect under a name a compiled-in collector also
// holds (aws, gcp, azure, k8s, github, gitlab, bitbucket) is dispatched to the
// entry. That is what "the entry name is the graph family" means at the
// dispatch: a registered family is collected by its entry wherever a builtin of
// that name would have been, and the builtin path serves the names no entry
// claims. A registration under a colliding name used to be ADMITTED and then
// silently ignored at collect time, reporting success while the internal
// collector ran instead.
//
// A BUILTIN GRAPH-TYPE NAME SHORT-CIRCUITS BEFORE THE FILES, and the reason is
// that its read is provably useless rather than merely expensive: a registration
// whose name matches kgtypes.IsBuiltinGraphType is refused on the write path by
// the SAME predicate, so no entry can serve one. code, web and pdf are the
// collect types this reaches, and code is this repo's own reflexive post-commit
// collect.
//
// A FAILED LOOKUP IS AN ERROR, NEVER AN ALL-CLEAR. A file that exists and cannot
// be read, or one whose contents are malformed, is a registration whose contents
// are unknown; treating that as "not registered" would send a custom collect
// down the builtin path to be refused as an unknown type — a message naming the
// wrong problem entirely.
//
// NOTHING RESOLVES FROM THE SERVER CATALOG. A catalog family with no file entry
// is LEGACY and is REFUSED, naming what to write; see refuseLegacyCatalogFamily.
func lookupCustomCollector(ctx context.Context, deps ClientDeps, collectorType string) (*resolvedCollector, error) {
	if kgtypes.IsBuiltinGraphType(collectorType) {
		return nil, nil // a builtin graph type can hold no registration.
	}
	loader, err := collectorLoader(ctx, deps)
	if err != nil {
		return nil, fmt.Errorf("collect %s: %w", collectorType, err)
	}
	se, found, err := loader.Resolve(collectorType)
	if err != nil {
		return nil, fmt.Errorf("collect %s: the custom-collector config could not be read, so whether this name is registered is unknown: %w", collectorType, err)
	}
	if !found {
		if err := refuseLegacyCatalogFamily(ctx, deps, loader, collectorType); err != nil {
			return nil, err
		}
		return nil, refuseUnreadProjectScope(loader, effectiveCwd(ctx, deps), collectorType)
	}
	// THIS ENTRY'S OWN UNRESOLVED REFERENCE, REFUSED BY NAME AND ONLY FOR THIS
	// ENTRY. The entry names a variable the process serving this collect does not
	// hold, so the child would be spawned with the literal `${NAME}` where its
	// credential belongs. It is refused here rather than at the load, which is
	// what keeps a sibling collector in the same file collectable: the message
	// names the file, the entry, the field and the variable, and says whose
	// environment supplies it.
	if se.Unresolved != nil {
		return nil, fmt.Errorf(
			"collect %s: %w. The value is supplied by the environment the process serving this "+
				"collect runs in, and no other entry in that file is affected",
			collectorType, se.Unresolved)
	}
	if err := upsertCollectorBehavior(ctx, deps, se); err != nil {
		return nil, fmt.Errorf("collect %s: %w", collectorType, err)
	}
	return &resolvedCollector{
		Registration: collectorconfig.Runtime(se.Name, se.Entry),
		Path:         se.Path,
	}, nil
}

// resolvedCollector is a runtime registration plus WHERE IT CAME FROM. The two
// travel together because every refusal on the collect path owes the operator
// the file as well as the family: the same name legitimately exists in both
// scopes, and the entry that failed is the one they have to edit.
type resolvedCollector struct {
	*externalcollector.Registration
	// Path is the absolute path of the scoped config file the entry was read
	// from. THE FAMILY NAME IS NOT CARRIED, deliberately: Loader.Resolve returns
	// only rows whose name equals the name it was asked for, so a second copy
	// here could never disagree with the collect's own type — and a field that
	// cannot disagree is a field no mutation can red. The wrap formats the family
	// from the collect's type instead.
	Path string
}

// upsertCollectorBehavior forwards the entry's BEHAVIOR half to the server, on
// every resolve, idempotently.
//
// THE SERVER NEEDS THIS RECORD AND NEEDS IT BADLY. Its graph-routing gate
// refuses a custom graph type it holds no behavior record for, and its whole
// summarize / embed / sync pipeline keys on the cascade — a family collected
// without one is readable by id and walkable and never enters the text index.
// The record carries the name and the behavior only: no CollectorSpec, so no env
// value and no header value is ever written to a graph-resident node.
//
// IT REPLACES A WIRE CALL RATHER THAN ADDING ONE: this path used to spend a
// whole-catalog browse per collect to find the record it now reads off disk.
//
// A nil CRUD client is CAPABILITY ABSENCE, not a failure: a degraded client
// legitimately has no catalog wired, and the collect's own sink will report what
// it cannot do. Update and Create compile to the identical mutate(upsert) wire
// call; Update is the name used here because every resolve after the first
// re-asserts a record the server already holds.
func upsertCollectorBehavior(ctx context.Context, deps ClientDeps, se collectorconfig.ScopedEntry) error {
	crud := deps.GraphTypeCRUD()
	if crud == nil {
		return nil
	}
	if err := crud.Update(ctx, collectorconfig.Persisted(se.Name, se.Entry)); err != nil {
		return fmt.Errorf("%s: collector %q: its behavior record could not be written to the server, so the family would collect but not be searchable: %w",
			se.Path, se.Name, err)
	}
	return nil
}

// refuseUnreadProjectScope is the second half of the not-found disposition, and
// it exists because of a message an operator would otherwise be handed wrongly.
//
// A SESSION WITH NO WORKSPACE CWD READS NO PROJECT FILE AT ALL. Its collect of a
// family registered in a repository's project scope falls to the builtin path,
// whose refusal is `collector: unknown collector "x"` — byte-identical to what a
// typo produces. The operator wrote the entry, can see it in their repository,
// and is told the collector does not exist. The daemon KNOWS at that moment that
// it never read a project file, so it says so.
//
// IT FIRES ONLY WHERE IT IS TRUE, and the guard is the important half: a name a
// COMPILED-IN COLLECTOR holds (aws, gcp, azure, k8s, github, gitlab, bitbucket,
// code, web, pdf, logs) must still reach that collector from a session with no
// cwd, so those fall through untouched. So does every name on a session that DID
// resolve a project scope — there the project file was read, the name is simply
// absent from it, and the generic unknown-type refusal is the correct answer.
func refuseUnreadProjectScope(loader collectorconfig.Loader, cwd, collectorType string) error {
	if loader.ProjectPath != "" {
		return nil // a project file WAS read; the name is genuinely absent from it.
	}
	if _, err := collector.Lookup(collectorType); err == nil {
		return nil // a compiled-in collector serves this name; it is not a missing entry.
	}
	userScope := loader.UserPath
	if userScope == "" {
		userScope = collectorconfig.UserPathIn("<home>")
	}
	// TWO WAYS TO HAVE READ NO PROJECT FILE, AND THEY ARE DIFFERENT FACTS. A
	// session with no working directory of its own never looked; a session that
	// has one looked and found nothing above it. Both leave a project-scope entry
	// invisible, and an operator's next move differs: the first is a client that
	// sends no cwd, the second is a directory outside the repository.
	where := "this session carries no workspace cwd, so NO project-scope file was read"
	if cwd != "" {
		where = fmt.Sprintf("no ancestor of this session's working directory (%s) holds one, so NO project-scope file was read", cwd)
	}
	return fmt.Errorf(
		"collect %s: no entry named %q in the user scope (%s), and %s — a project-scope entry at %s is invisible from here",
		collectorType, collectorType, userScope, where, collectorconfig.ProjectPathIn("<repo root>"))
}

// refuseLegacyCatalogFamily is the disposition for a name NO file entry claims.
//
// A FAMILY THE SERVER CATALOG STILL HOLDS IS REFUSED, NOT SERVED. The file is
// the registration record; a catalog record is a leftover from the retired
// register tool, and resolving from it would be a silent fallback to connection
// details this contract no longer owns. The refusal names three things so the
// operator's next step needs no documentation: the family, the absolute path of
// the file to write, and the `knowledge collector add` invocation that writes it.
//
// A NAME THE CATALOG DOES NOT HOLD EITHER falls through to the builtin path,
// which is what serves every compiled-in collector name and what names a truly
// unknown type.
//
// A CATALOG READ FAILURE IS AN ERROR, exactly as it was before this contract:
// the read can fail for reasons that have nothing to do with the name, and
// reporting "not registered" would send the collect on to be refused for the
// wrong reason.
func refuseLegacyCatalogFamily(ctx context.Context, deps ClientDeps, loader collectorconfig.Loader, collectorType string) error {
	crud := deps.GraphTypeCRUD()
	if crud == nil {
		return nil
	}
	def, found, err := crud.ByName(ctx, collectorType)
	if err != nil {
		return fmt.Errorf("collect %s: cannot tell whether a custom collector is registered under this name: %w", collectorType, err)
	}
	if !found {
		return nil
	}
	target := loader.UserPath
	if target == "" {
		target = collectorconfig.UserPathIn("<home>")
	}
	label, convertible := collectorconfig.LegacyClass(def)
	if !convertible {
		return fmt.Errorf("collect %s: the server catalog holds a %s record for this family and NOTHING resolves from the catalog — the config file is the registration record. That record names no tool, so no entry can be synthesized from it: write one at %s by hand (see the custom collector guide)",
			collectorType, label, target)
	}
	return fmt.Errorf("collect %s: the server catalog holds a %s record for this family and NOTHING resolves from the catalog — the config file is the registration record. Write the entry at %s, e.g. `%s`",
		collectorType, label, target, collectorconfig.LegacyInvocation(def, collectorconfig.ScopeUser))
}
