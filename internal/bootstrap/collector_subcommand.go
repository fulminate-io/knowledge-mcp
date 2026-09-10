// SPDX-License-Identifier: Apache-2.0

// collector_subcommand.go — `knowledge collector add | list | get | remove`, the
// install surface for a custom collector.
//
// IT MIRRORS `claude mcp` DELIBERATELY, down to the argv: options before the
// name, `--` before the command. Every MCP tool author already knows that shape,
// and the contract a custom collector is written against is the one they already
// know; the single addition is `--tool`, naming the one MCP tool the daemon
// calls to collect.
//
// NO VERB NEEDS A RUNNING DAEMON TO READ OR WRITE A FILE. add, get and remove
// touch files only. Two qualifications: `add` DIALS THE PROVIDER before writing
// (a connection to the provider, not to the daemon), and `list` reads the server
// catalog for its legacy column, which it reports as unreadable rather than
// failing the command or omitting the column.

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/graphtypecrud"
)

// collectorVerbs is the admitted verb set, rendered at call time by
// sortedCollectorVerbs so a refusal cannot enumerate a vocabulary the dispatch
// does not actually have.
var collectorVerbs = []string{"add", "get", "list", "remove"}

// runCollectorVerb dispatches the collector subcommand's verb.
func runCollectorVerb(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("collector: expected a verb — admitted: %s", strings.Join(sortedCollectorVerbs(), ", "))
	}
	switch args[0] {
	case "add":
		return runCollectorAdd(args[1:], os.Stdout)
	case "list":
		return runCollectorList(args[1:], os.Stdout)
	case "get":
		return runCollectorGet(args[1:], os.Stdout)
	case "remove":
		return runCollectorRemove(args[1:], os.Stdout)
	default:
		return fmt.Errorf("collector: unknown verb %q — admitted: %s", args[0], strings.Join(sortedCollectorVerbs(), ", "))
	}
}

// sortedCollectorVerbs renders the admitted set at call time.
func sortedCollectorVerbs() []string {
	out := append([]string(nil), collectorVerbs...)
	sort.Strings(out)
	return out
}

// collectorFlags are the flags every collector verb may accept. Not every verb
// registers all of them; the struct is shared so the parse and the render agree
// on one spelling per option.
type collectorFlags struct {
	scope     string
	transport string
	tool      string
	env       keyValueFlag
	headers   keyValueFlag
	port      int
	rest      []string

	// The behavior block's five values. The two booleans are TRI-STATE and the
	// three lists are repeatable; both properties are load-bearing rather than
	// stylistic — see triBoolFlag and listFlag.
	summarizable    triBoolFlag
	embeddable      triBoolFlag
	embedFields     listFlag
	summarizeFields listFlag
	bm25Fields      listFlag
}

// newCollectorFlagSet registers the flags one verb accepts.
//
// IT IS SEPARATE FROM THE PARSE so the registered set is READABLE without
// running a command, which is the same reason `check run` splits the two.
func newCollectorFlagSet(verb string, f *collectorFlags, withWriteFlags bool) *flag.FlagSet {
	fs := flag.NewFlagSet("collector "+verb, flag.ContinueOnError)
	fs.StringVar(&f.scope, "s", "", "scope: user (default) or project")
	fs.StringVar(&f.scope, "scope", "", "scope: user (default) or project")
	if verb == "list" {
		// THE PORT IS LIST'S ALONE, and its absence elsewhere is the readable form
		// of "no other verb talks to the daemon": add, get and remove touch files
		// only, so a port flag on them would advertise a connection they never make.
		fs.IntVar(&f.port, "port", graphclient.DefaultPort, "port the knowledge-server is listening on, for the legacy column")
	}
	if !withWriteFlags {
		return fs
	}
	fs.StringVar(&f.transport, "t", collectorconfig.TransportStdio, "transport: stdio (default) or http")
	fs.StringVar(&f.transport, "transport", collectorconfig.TransportStdio, "transport: stdio (default) or http")
	fs.StringVar(&f.tool, "tool", "", "the MCP tool the daemon calls on this provider to collect (required)")
	f.env = keyValueFlag{sep: "=", label: "-e KEY=VALUE"}
	f.headers = keyValueFlag{sep: ":", label: "-H 'Name: value'"}
	fs.Var(&f.env, "e", "environment variable for the stdio child, repeatable (KEY=VALUE)")
	fs.Var(&f.env, "env", "environment variable for the stdio child, repeatable (KEY=VALUE)")
	fs.Var(&f.headers, "H", "request header for the http provider, repeatable ('Name: value')")
	fs.Var(&f.headers, "header", "request header for the http provider, repeatable ('Name: value')")
	registerBehaviorFlags(fs, f)
	return fs
}

// parseCollectorFlags parses one verb's argv, leaving the positional arguments
// in rest.
//
// GO'S flag PACKAGE STOPS AT THE FIRST NON-FLAG ARGUMENT, and it honors `--` as
// a terminator ONLY before that point. In this command's shape the name comes
// after the options, so the `--` that separates the entry name from the
// provider's own command sits AFTER the first non-flag argument and SURVIVES
// into fs.Args() — which is what lets the provider's own flags (`--region
// us-east-1`) through without this parser eating them. The split on that literal
// `--` happens in the add handler.
func parseCollectorFlags(verb string, args []string, withWriteFlags bool) (collectorFlags, error) {
	var f collectorFlags
	fs := newCollectorFlagSet(verb, &f, withWriteFlags)
	if err := fs.Parse(args); err != nil {
		return collectorFlags{}, err
	}
	f.rest = fs.Args()
	return f, nil
}

// resolveScope maps the -s flag onto a scope. The DEFAULT IS USER: Claude's own
// default is `local`, a scope this design does not have (its local and user
// scopes both live in one home file), and user is the nearest — the machine's
// operator.
func resolveScope(raw string) (collectorconfig.Scope, error) {
	switch strings.TrimSpace(raw) {
	case "", string(collectorconfig.ScopeUser):
		return collectorconfig.ScopeUser, nil
	case string(collectorconfig.ScopeProject):
		return collectorconfig.ScopeProject, nil
	default:
		return "", fmt.Errorf("collector: unknown scope %q — it is %q or %q",
			raw, collectorconfig.ScopeUser, collectorconfig.ScopeProject)
	}
}

// cliLoader builds the loader this process reads: the machine's user file and
// the project file of the directory the operator is standing in.
//
// THE CLI'S OWN WORKING DIRECTORY IS THE BASE and it is always explicit — a
// person typing this command is standing somewhere on purpose — and there is no
// session cwd on this path.
// collectorHomeDir resolves the home directory the user scope lives under. It is
// a variable so a test can point it at a t.TempDir(): no suite in this
// repository may read or write the operator's real ~/.knowledge, and relocating
// HOME for the whole test process is not available — a storage location is a
// parameter, not a process-wide fact.
var collectorHomeDir = os.UserHomeDir

func cliLoader() (collectorconfig.Loader, error) {
	home, err := collectorHomeDir()
	if err != nil || home == "" {
		return collectorconfig.Loader{}, fmt.Errorf("collector: the home directory could not be resolved, so the user-scope config file cannot be located: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return collectorconfig.Loader{}, fmt.Errorf("collector: resolve the working directory: %w", err)
	}
	return collectorconfig.Loader{
		UserPath:    collectorconfig.UserPathIn(home),
		ProjectPath: collectorconfig.FindProjectPath(cwd),
	}, nil
}

// writeTargetPath resolves the file a write verb targets. For the PROJECT scope
// it is the file under the CURRENT directory when no ancestor already holds one,
// so `add -s project` in a fresh repository creates it where the operator is
// standing rather than refusing for want of a file that does not exist yet.
func writeTargetPath(loader collectorconfig.Loader, scope collectorconfig.Scope) (string, error) {
	if scope == collectorconfig.ScopeProject && loader.ProjectPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("collector: resolve the working directory: %w", err)
		}
		return collectorconfig.ProjectPathIn(cwd), nil
	}
	return loader.PathForScope(scope)
}

// runCollectorAdd writes one entry, after dialing the provider.
//
//	knowledge collector add [-s user|project] [-t stdio|http] --tool <name> \
//	    [-e KEY=VALUE]... [-H 'Name: value']... <name> -- <command> [args...]
//	knowledge collector add [-s ...] -t http --tool <name> [-H ...]... <name> <url>
func runCollectorAdd(args []string, w io.Writer) error {
	f, err := parseCollectorFlags("add", args, true)
	if err != nil {
		return err
	}
	scope, err := resolveScope(f.scope)
	if err != nil {
		return err
	}
	name, entry, err := entryFromArgs(f)
	if err != nil {
		return err
	}
	if err := graphtypecrud.ValidateName(name); err != nil {
		return fmt.Errorf("collector add: %w", err)
	}
	loader, err := cliLoader()
	if err != nil {
		return err
	}
	path, err := writeTargetPath(loader, scope)
	if err != nil {
		return err
	}
	// DIAL BEFORE WRITING. The tool's schemas are a hard requirement, and an entry
	// admitted without checking them would be a registration whose first proof of
	// correctness is a failed collect — naming a tool nobody could see at the time
	// they wrote it down. A refused provider leaves NO file written.
	//
	// THE DIAL ALSO RETURNS THE COLLECTOR'S DECLARATION, which is what the entry
	// is FILLED FROM. One dial does both: verifying against one connection and
	// filling from a second would write an entry from a state nobody observed.
	decl, err := dialForAdd(name, path, entry)
	if err != nil {
		return err
	}
	entry, skippedSecrets := collectorconfig.ApplyDeclaration(entry, decl, os.LookupEnv)
	// The filled entry has fields the operator never typed, so it meets the
	// loader's refusals again HERE, before it is written: a declaration this
	// client cannot honor must fail the add rather than the next load.
	if err := collectorconfig.ValidateEntry(path, name, entry); err != nil {
		return err
	}
	if err := collectorconfig.Upsert(path, name, entry); err != nil {
		return err
	}
	rendered, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("collector add: render the written entry: %w", err)
	}
	fmt.Fprintf(w, "collector %q added in the %s scope\nfile: %s\n%s\n", name, scope, path, rendered)
	writeDeclarationNotes(w, name, decl, skippedSecrets)
	return nil
}

// writeDeclarationNotes prints what the collector SUGGESTED and what the add
// deliberately did not write.
//
// THE SUGGESTION IS PRINTED AND NOT APPLIED, and printing it is the whole reason
// a collector is allowed to make one. Summarizing and embedding are spend on the
// operator's account: the decision is theirs, and an operator who cannot see
// what the collector's author thinks is worth paying for has to read the module
// README to find out. The line says which flags turn each one on.
//
// THE SKIPPED SECRETS ARE NAMED FOR THE SAME REASON THE INSTALLER NAMES THEM:
// someone who exported a credential and finds it absent from the entry is owed
// the reason. NAMES ONLY, NEVER VALUES.
func writeDeclarationNotes(w io.Writer, name string, decl *externalcollector.Declaration, skippedSecrets []string) {
	if decl == nil {
		return
	}
	if b := decl.Behavior; b != nil && b.Summarizable != nil && b.Embeddable != nil {
		fmt.Fprintf(w,
			"collector %q SUGGESTS summarizable=%t embeddable=%t — not applied: the two LLM axes are yours, and this add "+
				"wrote what your flags said (--summarize / --embed turn them on)\n",
			name, *b.Summarizable, *b.Embeddable)
	}
	if len(skippedSecrets) > 0 {
		fmt.Fprintf(w,
			"collector %q declares these credential names, and NONE of them was written into the entry in any form: %s\n",
			name, strings.Join(skippedSecrets, ", "))
	}
}

// dialForAdd runs the record-shape check and then the live contract check
// against the entry as written, EXPANDED, so what is dialed is what a collect
// will dial. It returns the provider's own declaration, which the caller fills
// the entry from.
func dialForAdd(name, path string, entry collectorconfig.Entry) (*externalcollector.Declaration, error) {
	expanded, err := collectorconfig.ExpandForDial(path, name, entry)
	if err != nil {
		return nil, err
	}
	// The entry came off argv rather than out of a file, so it has met none of the
	// loader's refusals yet. Run them HERE: an entry refused at the next load is
	// an entry an operator was told they had installed.
	if err := collectorconfig.ValidateEntry(path, name, expanded); err != nil {
		return nil, err
	}
	reg := collectorconfig.Runtime(name, expanded)
	// The shape validation runs first so a malformed entry is refused with no
	// network round trip.
	if err := graphtypecrud.ValidateCollector(reg.Def.GetCollector()); err != nil {
		return nil, fmt.Errorf("collector add: %q: %w", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), collectorDialTimeout)
	defer cancel()
	decl, err := externalcollector.VerifyRegistration(ctx, reg)
	if err != nil {
		return nil, fmt.Errorf("collector add: %q was NOT written: %w", name, err)
	}
	return decl, nil
}

// collectorDialTimeout bounds the provider dial `add` performs. A provider that
// never answers must fail the command rather than hang it.
const collectorDialTimeout = 30 * time.Second

// entryFromArgs builds the entry from one add invocation's parsed argv.
func entryFromArgs(f collectorFlags) (string, collectorconfig.Entry, error) {
	if len(f.rest) == 0 {
		return "", collectorconfig.Entry{}, errors.New("collector add: expected a name — `knowledge collector add [options] <name> -- <command> [args...]`")
	}
	name := f.rest[0]
	rest := f.rest[1:]
	entry := collectorconfig.Entry{
		Type:     strings.TrimSpace(f.transport),
		Tool:     strings.TrimSpace(f.tool),
		Env:      f.env.asMap(),
		Headers:  f.headers.asMap(),
		Behavior: behaviorFromFlags(f),
	}
	if entry.Tool == "" {
		return "", collectorconfig.Entry{}, errors.New("collector add: --tool is required — it names the single MCP tool the daemon calls on this provider to collect")
	}
	switch entry.Type {
	case collectorconfig.TransportStdio:
		command, cmdArgs, err := splitAfterDoubleDash(rest)
		if err != nil {
			return "", collectorconfig.Entry{}, err
		}
		entry.Command, entry.Args = command, cmdArgs
	case collectorconfig.TransportHTTP:
		if len(rest) != 1 {
			return "", collectorconfig.Entry{}, errors.New("collector add: an http collector takes exactly one positional argument after the name: its url")
		}
		entry.URL = rest[0]
	default:
		return "", collectorconfig.Entry{}, fmt.Errorf("collector add: unknown transport %q — it is %q or %q",
			entry.Type, collectorconfig.TransportStdio, collectorconfig.TransportHTTP)
	}
	return name, entry, nil
}

// splitAfterDoubleDash takes the provider command and its arguments from the
// positional tail, which begins with the literal `--` this parser deliberately
// did not consume.
func splitAfterDoubleDash(rest []string) (string, []string, error) {
	if len(rest) == 0 || rest[0] != "--" {
		return "", nil, errors.New("collector add: a stdio collector's command follows a `--` after the name: `knowledge collector add [options] <name> -- <command> [args...]`")
	}
	tail := rest[1:]
	if len(tail) == 0 {
		return "", nil, errors.New("collector add: the `--` is followed by no command")
	}
	if len(tail) == 1 {
		return tail[0], nil, nil
	}
	return tail[0], append([]string(nil), tail[1:]...), nil
}

// runCollectorRemove deletes one entry from its scoped file.
func runCollectorRemove(args []string, w io.Writer) error {
	f, err := parseCollectorFlags("remove", args, false)
	if err != nil {
		return err
	}
	if len(f.rest) != 1 {
		return errors.New("collector remove: expected exactly one name — `knowledge collector remove <name> [-s user|project]`")
	}
	scope, err := resolveScope(f.scope)
	if err != nil {
		return err
	}
	loader, err := cliLoader()
	if err != nil {
		return err
	}
	path, err := loader.PathForScope(scope)
	if err != nil {
		return err
	}
	if err := collectorconfig.Remove(path, f.rest[0]); err != nil {
		return fmt.Errorf("collector remove: %w", err)
	}
	fmt.Fprintf(w, "collector %q removed from the %s scope\nfile: %s\n", f.rest[0], scope, path)
	return nil
}

// catalogLister reads what the SERVER holds, for the legacy column. It is a
// parameter so the rendering is testable without a daemon: no test in this repo
// contacts the operator's running server.
type catalogLister func(ctx context.Context) ([]*knowledgev1.GraphTypeDef, error)

// runCollectorList renders every entry from both scopes plus the legacy column.
func runCollectorList(args []string, w io.Writer) error {
	f, err := parseCollectorFlags("list", args, false)
	if err != nil {
		return err
	}
	loader, err := cliLoader()
	if err != nil {
		return err
	}
	return renderCollectorList(w, loader, liveCatalogLister(f.port))
}

// liveCatalogLister returns the catalog reader backed by the local daemon.
//
// THE COMMAND HAS TWO SOURCES AND THE CALL SITE PASSES BOTH: the config-file
// loader, which is what `collector list` lists, and this reader, which supplies
// one column beside it. Reading the two as one is what makes the site's routing
// look like a question when it is not; the declaration inside the literal below
// speaks for this half only.
func liveCatalogLister(port int) catalogLister {
	return func(ctx context.Context) ([]*knowledgev1.GraphTypeDef, error) {
		// routing: local by design — the operator's own daemon on the configured
		// port. `collector list` is local on every backend by an owner decision of
		// 2026-09-09; what this client fetches is the GraphTypeDef catalog for the
		// listing's second column, and the config file the ruling names is read by
		// the loader passed beside it.
		gc := graphclient.NewGraphClient(port)
		healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		healthy := gc.HealthyCtx(healthCtx)
		cancel()
		if !healthy {
			return nil, fmt.Errorf("knowledge-server is not running on port %d", port)
		}
		return graphtypecrud.New(gc).List(ctx)
	}
}
