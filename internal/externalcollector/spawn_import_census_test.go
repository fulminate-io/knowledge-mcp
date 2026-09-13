// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spawn_import_census_test.go — THE COARSE CARRIER for "no code path executes a
// registered binary directly".
//
// WHY AN IMPORT CENSUS AND NOT MORE PATTERN CHECKS. The requirement is an
// EFFECT: a command assembled from a registered record must reach the operating
// system only through the SDK transport in mcphost.go. The structural checks in
// the corpus track SPELLINGS of that effect — exec.Command, exec.CommandContext,
// an &exec.Cmd literal, an aliased import of the package — and each audit round
// found the next spelling, because a pattern check can only know the shapes its
// author thought of. This test knows none of them and does not need to: a
// process cannot be spawned from Go without importing one of the packages that
// can spawn it, so pinning WHO IMPORTS THOSE PACKAGES bounds the whole class at
// once. A new spelling in an already-listed file is still invisible here — that
// is what the pattern checks are for, and why they stay — but a new FILE that
// spawns anything at all is caught whatever it writes.
//
// WHY IT IS A TEST AND NOT A CORPUS CHECK, and the reason is what a CHECK
// cannot carry rather than what the pattern language cannot see. The pattern
// language CAN express "which files import a spawning package": `$$$P
// "os/exec"` binds an import spec in both declaration shapes, grouped and
// ungrouped, aliased and plain, and a corpus check does exactly that class
// detection. What a check cannot express is everything else this test asserts —
// a per-file allowlist carrying the REASON each entry may spawn, the
// stale-entry direction that keeps the list a census rather than a standing
// permission, the known positive proving the walk read the right tree, and the
// package-scoped contract below. A check node carries one pattern, one
// where-tree, a severity and a test-scope flag; none of its leaves reads a file
// path, and a check reports sites where a SHAPE IS PRESENT, which has no
// vocabulary for "this named file must still contain X".
//
// Two facts about the pattern language that remain true and shaped the original
// authoring: a bare capture compiles only to the stmt and expr contexts, and a
// placeholder standing for a WHOLE SPEC does not compile in a spec slot at all
// (a bare identifier is a legal spec NAME, so the parse reads it as one). The
// single-spec claim is the one that no longer holds.
//
// This test is the same census by the instrument that can run all four
// assertions — go/parser over the module, which is exact rather than
// approximate, and which sees an aliased or blank import exactly as it sees a
// plain one.
//
// SCOPE: non-test files only. A _test.go file is not compiled into the shipped
// binary and cannot carry a registered record to a user's machine; the harnesses
// legitimately spawn stub providers, compilers and child processes, and listing
// them would be a churn surface with nothing behind it.

// spawnPackages are the import paths from which a process can be started.
// os/exec is the ordinary route; os and syscall reach the primitives below it
// (os.StartProcess, syscall.Exec, syscall.ForkExec) and are covered by their own
// corpus check on the CALL, because os in particular is imported by nearly every
// file for reasons that have nothing to do with spawning.
var spawnPackages = map[string]bool{"os/exec": true}

// spawnImporters is the ALLOWLIST: every non-test file in this module that may
// import a spawning package, with the reason it legitimately needs one. A file
// absent from this map that imports os/exec fails this test; a file listed here
// that no longer imports it fails too, so the list cannot rot into a permission
// slip for code that has moved on.
var spawnImporters = map[string]string{
	// THE ONE THAT MATTERS FOR THE CUSTOM-COLLECTOR CONTRACT. The registered
	// record's command is resolved and handed to the SDK's CommandTransport
	// here and nowhere else. That this file is the ONLY externalcollector file
	// on the list is the assertion the contract actually rests on.
	"internal/externalcollector/mcphost.go": "resolves the record-derived command and hands it to the MCP SDK's CommandTransport",

	// Operator-facing subprocesses. Every command below is a LITERAL or comes
	// from the operator's own environment, configuration or argv; none takes an
	// argument from a registered graph-type record.
	"internal/auth/browser_flow.go":                    "opens the operator's browser for the device-code login (open / xdg-open / rundll32)",
	"internal/bootstrap/client_update_check.go":        "asks brew whether a newer client is installed",
	"internal/bootstrap/client_update_handoff_argv.go": "re-executes this CLI's own binary after an update",
	"internal/bootstrap/install.go":                    "runs the installed binary's --version to confirm the install",
	"internal/bootstrap/install_claude_assets.go":      "runs diff -u to show the operator what an asset update would change",
	"internal/bootstrap/lifecycle.go":                  "starts the local daemon this CLI manages",
	"internal/bootstrap/lifecycle_subcommand.go":       "reads launchctl list to report the managed service's state",
	"internal/bootstrap/mcp_register.go":               "resolves this CLI's own binary to write into the MCP client registration",
	"internal/bootstrap/service_manager.go":            "runs launchctl, systemctl, brew, lsof or netstat for Knowledge lifecycle management, and probes installed Knowledge binary capabilities",
	"internal/bootstrap/service_runtime.go":            "starts the locally resolved Knowledge client or backend for the service subcommand; no registered collector record supplies the command",
	"internal/bootstrap/setup.go":                      "looks up the operator's installed agent CLIs during setup",
	"internal/bootstrap/setup_restart.go":              "reads lsof and systemctl to find and restart the running daemon",
	"internal/bootstrap/setup_service.go":              "drives launchctl / systemctl / loginctl for the managed service definition",
	"internal/cli/tunnel_ssh.go":                       "runs the operator's ssh for the tunnel subcommand",
	"internal/collector/coderun/git.go":                "reads git branch, HEAD and diff state for the repository being collected",
	"internal/collector/parser/indexer_discover.go":    "reads git for the file list of the repository being collected",
	"internal/filecrypt/machineid/machine.go":          "reads ioreg for the machine identity the file-store key is derived from",
	"internal/graphclient/peer_cwd.go":                 "runs a peer command in a named working directory",
	"internal/llm/claudecli/claudecli.go":              "resolves the operator's own installed agent CLI on PATH",
	"internal/llm/claudecli/subprocess.go":             "runs the operator's own installed agent CLI",
	"internal/llm/codexcli/codexcli.go":                "resolves the operator's own installed agent CLI on PATH",
	"internal/llm/codexcli/subprocess.go":              "runs the operator's own installed agent CLI",

	// THESE FILES SPAWN NOTHING AT ALL, and they are on the list because the
	// census is an IMPORT census: importing the package is the thing it can
	// see, and a file that only names a sentinel or a function value imports it
	// exactly as a file that runs a command does. Listing them with what they
	// actually do is the honest form — pretending the import implies a spawn
	// would make the list say something untrue about them.
	"internal/auth/storage_select.go":               "compares an error against exec.ErrNotFound; spawns nothing",
	"internal/bootstrap/subcommands.go":             "unwraps an *exec.ExitError to propagate a child's exit code; spawns nothing itself",
	"internal/config/autodetect.go":                 "takes exec.LookPath as an injectable function value for PATH detection; spawns nothing",
	"internal/bootstrap/service_process_unix.go":    "names *exec.Cmd in the Unix service configuration hook; spawns nothing itself",
	"internal/bootstrap/service_process_windows.go": "sets *exec.Cmd creation flags for independent Knowledge sessions on Windows; spawns nothing itself",
}

func TestSpawningPackagesAreImportedOnlyWhereTheyBelong(t *testing.T) {
	root := discoverModuleRoot(t)

	found := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		for _, spec := range file.Imports {
			// The unquoted PATH is what is read, never the local name: an
			// aliased or blank import is the same import, and reading the alias
			// is exactly how a spelling-based audit gets fooled.
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if spawnPackages[imported] {
				found[rel] = append(found[rel], imported)
			}
		}
		return nil
	})
	require.NoError(t, err)

	// KNOWN POSITIVE: the walk actually read this module. Without it an empty
	// census — a mistyped root, a skip rule that ate everything — reads as a
	// clean result, which is the failure mode this whole test exists to avoid.
	require.Contains(t, found, "internal/externalcollector/mcphost.go",
		"the census did not see the one file it is certain about; the walk read the wrong tree")

	var unexpected []string
	for file := range found {
		if _, ok := spawnImporters[file]; !ok {
			unexpected = append(unexpected, file)
		}
	}
	sort.Strings(unexpected)
	assert.Empty(t, unexpected,
		"these files import a process-spawning package and are not on the allowlist. If the import is legitimate, "+
			"add it with the reason it needs to spawn; if it is a command built from a registered record, it belongs "+
			"behind the MCP transport in mcphost.go instead: %v", unexpected)

	var stale []string
	for file := range spawnImporters {
		if _, ok := found[file]; !ok {
			stale = append(stale, file)
		}
	}
	sort.Strings(stale)
	assert.Empty(t, stale,
		"these files are on the spawn allowlist but no longer import a spawning package; remove them so the list "+
			"stays a census rather than a standing permission: %v", stale)

	// THE CONTRACT'S OWN ASSERTION, stated separately from the module-wide one:
	// within this package, mcphost.go is the only file that may spawn. A helper
	// that grew its own exec call would satisfy the allowlist above only by
	// being added to it, and this line is what makes that addition a deliberate
	// act rather than a quiet one.
	for file := range found {
		if strings.HasPrefix(file, "internal/externalcollector/") {
			assert.Equal(t, "internal/externalcollector/mcphost.go", file,
				"the custom-collector package spawns from mcphost.go alone")
		}
	}
}

// discoverModuleRoot walks up from this package to the directory holding the
// go.mod of the client module. It is discovered rather than named by a fixed
// number of "..", for the same reason the docs-link sentinel discovers its
// target: a hardcoded depth breaks under a different checkout layout for a
// reason that is not the defect the test exists to catch.
func discoverModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no ancestor of the package directory holds a go.mod; the module root could not be located")
		}
		dir = parent
	}
}
