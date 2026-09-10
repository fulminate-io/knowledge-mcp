// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collector_subcommand_test.go — `knowledge collector add | list | get | remove`.
//
// NO TEST HERE TOUCHES THE OPERATOR'S ~/.knowledge. Every one points
// collectorHomeDir at a t.TempDir() and, where a project scope is involved,
// chdirs into a scratch tree. Relocating HOME for the whole process is not
// available in this repository, which is exactly why the home resolver is a
// variable.
//
// AND NO TEST HERE CONTACTS A DAEMON. The only verb that reads the server is
// list, whose catalog reader is a parameter; the others make no connection at
// all, which is asserted rather than assumed.

// contractStubTool is the tool every provider in this file advertises and every
// entry it writes names.
const contractStubTool = "collect"

// The STDIO stub provider: this test binary, re-execed. `knowledge collector add`
// dials before writing, so an add that must actually WRITE a stdio entry needs a
// stdio provider that answers a handshake — and a child that is this binary needs
// no compile step, no scaffolding and no network.
//
// THE MARKER RIDES ARGV AND THE MODE RIDES THE ENTRY'S ENV BLOCK, which is the
// same split the externalcollector harness uses and for the same reason: the
// block is the thing under test, so a child that did not receive it must be able
// to say so instead of falling through to running this suite and spawning more
// children.
const (
	cliStubArgvMarker = "--ful1794-cli-stub-provider"
	cliStubModeEnv    = "FUL1794_CLI_STUB_MODE"
	// cliStubNoDescribeEnv makes the stdio child serve the collect tool alone,
	// which is every provider written before the describe tool existed.
	cliStubNoDescribeEnv = "FUL1794_CLI_STUB_NO_DESCRIBE"
)

// maybeRunCollectorStubProvider serves MCP over stdio when this process was
// spawned as the collector stub, and returns immediately in a normal run.
func maybeRunCollectorStubProvider() {
	if len(os.Args) < 2 || os.Args[1] != cliStubArgvMarker {
		return
	}
	if os.Getenv(cliStubModeEnv) == "" {
		fmt.Fprintf(os.Stderr,
			"cli stub provider: spawned as a child (%s) with %s UNSET — the entry's env block did not reach it. "+
				"Exiting rather than running the test suite, which would spawn more children.\n",
			cliStubArgvMarker, cliStubModeEnv)
		os.Exit(9)
	}
	in := map[string]any{}
	if err := json.Unmarshal(externalcollector.InputContractJSON(), &in); err != nil {
		fmt.Fprintf(os.Stderr, "cli stub provider: %v\n", err)
		os.Exit(1)
	}
	out := map[string]any{}
	if err := json.Unmarshal(externalcollector.OutputContractJSON(), &out); err != nil {
		fmt.Fprintf(os.Stderr, "cli stub provider: %v\n", err)
		os.Exit(1)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "ful1794-cli-stdio-stub", Version: "v1"}, nil)
	server.AddTool(&mcp.Tool{Name: contractStubTool, Description: "stub", InputSchema: in, OutputSchema: out},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: map[string]any{
				"nodes": []any{}, "edges": []any{}, "walk_complete": true,
			}}, nil
		})
	// THE DESCRIBE TOOL IS REQUIRED, so the stub serves it unless a case asks for
	// a provider that does not — which is itself a row: a describe-less provider
	// must be refused by name and write nothing.
	if os.Getenv(cliStubNoDescribeEnv) == "" {
		addStubDescribeTool(server, nil, nil)
	}
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "cli stub provider: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// useTempHome points the CLI's user scope at a scratch home and returns its
// collectors.json path.
func useTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := collectorHomeDir
	collectorHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { collectorHomeDir = prev })
	return collectorconfig.UserPathIn(home)
}

// useScratchCwd chdirs into a fresh directory for one test, so the project-scope
// walk-up starts somewhere this test owns.
func useScratchCwd(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })
	// macOS reports /var as a symlink to /private/var, so the resolved path is
	// what a later comparison against os.Getwd() output must use.
	resolved, err := os.Getwd()
	require.NoError(t, err)
	return resolved
}

// startContractProvider stands up a provider whose tool satisfies the collector
// contract, so `add` dials something real. conform=false advertises an output
// schema missing the completeness assertion, which is the refusal arm.
//
// The tool name is fixed: every case here writes an entry naming contractStubTool,
// and varying it would test the stub rather than the command.
func startContractProvider(t *testing.T, conform bool, opts ...describeStubOption) string {
	tool := contractStubTool
	t.Helper()
	in := map[string]any{}
	require.NoError(t, json.Unmarshal(externalcollector.InputContractJSON(), &in))
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(externalcollector.OutputContractJSON(), &out))
	if !conform {
		out["required"] = []any{"nodes", "edges"}
		delete(out["properties"].(map[string]any), "walk_complete")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "ful1794-cli-stub", Version: "v1"}, nil)
	server.AddTool(&mcp.Tool{Name: tool, Description: "stub", InputSchema: in, OutputSchema: out},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: map[string]any{
				"nodes": []any{}, "edges": []any{}, "walk_complete": true,
			}}, nil
		})
	cfg := describeStubConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if !cfg.omitTool {
		addStubDescribeTool(server, cfg.schema, cfg.declaration)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		srv.Close()
	})
	return srv.URL
}

// TestCliStubHarness_AMarkedChildWithNoModeExitsInsteadOfRunningTheSuite is this
// package's copy of the externalcollector harness guard, and it is here for the
// same measured reason: a re-execed child that decides "am I the child?" from a
// channel the change under test can break is one mutation away from unbounded
// spawning. The mode rides the entry's env block, which is the thing under test;
// the marker rides argv, which this contract never scrubs.
func TestCliStubHarness_AMarkedChildWithNoModeExitsInsteadOfRunningTheSuite(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, cliStubArgvMarker) //nolint:gosec // the test binary's own path.
	cmd.Env = []string{}                                     // the block did not arrive
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	require.Error(t, runErr, "a marked child with no mode must FAIL rather than run the suite")
	var exit *exec.ExitError
	require.ErrorAs(t, runErr, &exit)
	assert.Equal(t, 9, exit.ExitCode(), "the guard's own exit code, so the cause is readable from a process table")
	assert.Contains(t, stderr.String(), cliStubModeEnv, "and it must name the variable that did not arrive")
	assert.NotContains(t, stderr.String(), "--- PASS", "it must not have run a single test")
}

// TestCollectorAdd_WritesTheEntryAndPrintsItBack is the add verb's happy path on
// the http transport, driven through the real argv.
func TestCollectorAdd_WritesTheEntryAndPrintsItBack(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)
	url := startContractProvider(t, true)

	var out bytes.Buffer
	require.NoError(t, runCollectorAdd([]string{
		"-t", "http", "--tool", "collect", "-H", "Authorization: Bearer x", "remote", url,
	}, &out))

	body := out.String()
	assert.Contains(t, body, "remote")
	assert.Contains(t, body, userPath, "the answer must name the absolute path it wrote")
	assert.Contains(t, body, "user scope", "and the scope it wrote into")

	loaded, found, err := collectorconfig.Loader{UserPath: userPath}.Resolve("remote")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, collectorconfig.TransportHTTP, loaded.Entry.Type)
	assert.Equal(t, url, loaded.Entry.URL)
	assert.Equal(t, "collect", loaded.Entry.Tool)
	assert.Equal(t, map[string]string{"Authorization": "Bearer x"}, loaded.Entry.Headers)
}

// TestCollectorAdd_WritesTheREFERENCEAndNeverTheResolvedValue is the CLI half of
// the contract expansion exists for: an operator keeps a credential out of a
// file that can live at a repository root by naming the variable instead.
//
// THE ASSERTION IS ON THE FILE BYTES, both arms, because the failure is
// invisible anywhere else: `add` DIALS with the expanded entry, so a write path
// that stored what it dialed would work perfectly and quietly bake the secret
// into the file. Nothing in this suite carried a ${ reference before this test,
// so the mutation had nothing to expand.
//
// THE SAME-RUN CONTROL is the second half of each arm: a LOAD does expand the
// reference. Without it, an assertion that the resolved value is absent from the
// file is equally satisfied by an expander that never fires at all.
func TestCollectorAdd_WritesTheREFERENCEAndNeverTheResolvedValue(t *testing.T) {
	t.Setenv("FUL1794_CLI_TOKEN", "super-secret-cli")

	t.Run("http headers", func(t *testing.T) {
		userPath := useTempHome(t)
		useScratchCwd(t)
		url := startContractProvider(t, true)

		var out bytes.Buffer
		require.NoError(t, runCollectorAdd([]string{
			"-t", "http", "--tool", contractStubTool,
			"-H", "Authorization: Bearer ${FUL1794_CLI_TOKEN}", "remote", url,
		}, &out))

		raw, err := os.ReadFile(userPath) //nolint:gosec // a t.TempDir() path.
		require.NoError(t, err)
		body := string(raw)
		assert.Contains(t, body, "${FUL1794_CLI_TOKEN}", "the reference must be written verbatim")
		assert.NotContains(t, body, "super-secret-cli", "the resolved value must NEVER be written into the file")

		loaded, found, err := collectorconfig.Loader{UserPath: userPath}.Resolve("remote")
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "Bearer super-secret-cli", loaded.Entry.Headers["Authorization"],
			"the CONTROL: a load does expand it, so the assertions above are about the write path")
	})

	t.Run("stdio env", func(t *testing.T) {
		userPath := useTempHome(t)
		useScratchCwd(t)

		var out bytes.Buffer
		// The provider is never dialed for this arm: /bin/true answers no
		// handshake, so the add is expected to FAIL — and the point of the arm is
		// what it wrote, which must be nothing. The verbatim-reference assertion
		// therefore rides the http arm above; this one pins that a refused add
		// leaves no file at all, expanded or otherwise.
		err := runCollectorAdd([]string{
			"--tool", contractStubTool, "-e", "TOKEN=${FUL1794_CLI_TOKEN}",
			"tickets", "--", "/bin/true",
		}, &out)
		require.Error(t, err, "a provider that answers no handshake must be refused")

		_, statErr := os.Stat(userPath)
		assert.True(t, os.IsNotExist(statErr), "a refused add must write no file, so no value can have been baked into one")
	})

	t.Run("stdio env, written through a dialable provider", func(t *testing.T) {
		userPath := useTempHome(t)
		useScratchCwd(t)
		// A REAL stdio provider is what makes this arm write anything: it is this
		// test binary re-execed in the CLI stub mode, so the add completes its
		// dial and reaches the write site the mutation targets.
		self, err := os.Executable()
		require.NoError(t, err)

		var out bytes.Buffer
		err = runCollectorAdd([]string{
			"--tool", contractStubTool,
			"-e", cliStubModeEnv + "=1", "-e", "TOKEN=${FUL1794_CLI_TOKEN}",
			"tickets", "--", self, cliStubArgvMarker,
		}, &out)
		require.NoError(t, err, out.String())

		raw, err := os.ReadFile(userPath) //nolint:gosec // a t.TempDir() path.
		require.NoError(t, err)
		body := string(raw)
		assert.Contains(t, body, "${FUL1794_CLI_TOKEN}", "the reference must be written verbatim")
		assert.NotContains(t, body, "super-secret-cli", "the resolved value must NEVER be written into the file")

		loaded, found, err := collectorconfig.Loader{UserPath: userPath}.Resolve("tickets")
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "super-secret-cli", loaded.Entry.Env["TOKEN"],
			"the CONTROL: a load does expand it")
	})
}

// TestCollectorAdd_ArgvMatrix is the row this command most needs, because a
// hand-rolled parser mis-parses SILENTLY.
//
// THE `--` SURVIVES INTO fs.Args() and that is load-bearing: Go's flag package
// stops at the first non-flag argument, and the entry name is that argument, so
// the terminator after it is never consumed and the provider's own flags reach
// the entry rather than this parser.
func TestCollectorAdd_ArgvMatrix(t *testing.T) {
	for _, tc := range []struct {
		name    string
		argv    []string
		want    collectorconfig.Entry
		wantKey string
	}{
		{
			"options before the name, command after --, provider flags untouched",
			[]string{"-t", "stdio", "--tool", "collect", "tickets", "--", "/usr/local/bin/p", "--region", "us-east-1"},
			collectorconfig.Entry{Type: "stdio", Tool: "collect", Command: "/usr/local/bin/p", Args: []string{"--region", "us-east-1"}},
			"tickets",
		},
		{
			"a command with no arguments",
			[]string{"--tool", "collect", "tickets", "--", "/usr/local/bin/p"},
			collectorconfig.Entry{Type: "stdio", Tool: "collect", Command: "/usr/local/bin/p"},
			"tickets",
		},
		{
			"an -e value containing = splits on the FIRST one only",
			[]string{"--tool", "collect", "-e", "URL=https://x?a=b", "tickets", "--", "/p"},
			collectorconfig.Entry{Type: "stdio", Tool: "collect", Command: "/p", Env: map[string]string{"URL": "https://x?a=b"}},
			"tickets",
		},
		{
			"repeated -e",
			[]string{"--tool", "collect", "-e", "A=1", "-e", "B=2", "tickets", "--", "/p"},
			collectorconfig.Entry{Type: "stdio", Tool: "collect", Command: "/p", Env: map[string]string{"A": "1", "B": "2"}},
			"tickets",
		},
		{
			"an -H value containing a colon splits on the FIRST one only",
			[]string{"-t", "http", "--tool", "collect", "-H", "X-Trace: id:42", "remote", "https://c.example/mcp"},
			collectorconfig.Entry{Type: "http", Tool: "collect", URL: "https://c.example/mcp", Headers: map[string]string{"X-Trace": "id:42"}},
			"remote",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseCollectorFlags("add", tc.argv, true)
			require.NoError(t, err)
			name, entry, err := entryFromArgs(f)
			require.NoError(t, err)
			assert.Equal(t, tc.wantKey, name)
			assert.Equal(t, tc.want, entry)
		})
	}
}

// TestCollectorAdd_ArgvRefusals covers the shapes the parser must refuse rather
// than half-accept.
func TestCollectorAdd_ArgvRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		argv       []string
	}{
		{"no name", "expected a name", []string{"--tool", "collect"}},
		{"no --tool", "--tool is required", []string{"tickets", "--", "/p"}},
		{"a stdio entry with no --", "follows a `--`", []string{"--tool", "collect", "tickets", "/p"}},
		{"a -- with no command", "followed by no command", []string{"--tool", "collect", "tickets", "--"}},
		{"an http entry with no url", "exactly one positional", []string{"-t", "http", "--tool", "collect", "remote"}},
		{"an unknown transport", "unknown transport", []string{"-t", "sse", "--tool", "collect", "remote", "https://x/"}},
		{"an -e with no =", "not =-separated", []string{"--tool", "collect", "-e", "JUST_A_NAME", "tickets", "--", "/p"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseCollectorFlags("add", tc.argv, true)
			if err != nil {
				assert.Contains(t, err.Error(), tc.want)
				return
			}
			_, _, err = entryFromArgs(f)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestCollectorAdd_DialsBeforeWritingAndARefusalWritesNothing is the live half
// of the contract check, at the venue it moved to.
//
// THE KNOWN POSITIVE IS IN THE SAME RUN: a conforming provider IS written. Without
// it, the empty file below is equally explained by a write path that never works.
func TestCollectorAdd_DialsBeforeWritingAndARefusalWritesNothing(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)

	good := startContractProvider(t, true)
	var out bytes.Buffer
	require.NoError(t, runCollectorAdd([]string{"-t", "http", "--tool", "collect", "good", good}, &out))

	bad := startContractProvider(t, false)
	err := runCollectorAdd([]string{"-t", "http", "--tool", "collect", "bad", bad}, &out)
	require.Error(t, err, "a provider whose tool does not satisfy the contract must be refused BEFORE the entry is written")
	assert.Contains(t, err.Error(), "walk_complete", "the refusal must name the mismatch")
	assert.Contains(t, err.Error(), "NOT written")

	entries, lerr := collectorconfig.ReadRaw(userPath)
	require.NoError(t, lerr)
	_, wrote := entries["bad"]
	assert.False(t, wrote, "a refused add must leave NO entry behind")
	_, kept := entries["good"]
	assert.True(t, kept, "and the known positive must still be there")
}

// TestCollectorAdd_RefusesABuiltinGraphTypeName pins that the name gate runs at
// the CLI. Letting the file take the name and failing at the next collect would
// leave an operator holding an entry that can never run.
func TestCollectorAdd_RefusesABuiltinGraphTypeName(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)

	var out bytes.Buffer
	err := runCollectorAdd([]string{"--tool", "collect", "code", "--", "/bin/true"}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code")
	assert.Contains(t, err.Error(), "built-in graph type")

	_, statErr := os.Stat(userPath)
	assert.True(t, os.IsNotExist(statErr), "a refused name must not have created the file")
}

// TestCollectorAdd_RefusesARetiredBuiltinGraphTypeName is the second half of the
// name gate. A RETIRED builtin stops being claimed by the builtin predicate, so
// without its own check the name becomes free and a custom family could adopt
// the leftover directory an upgrading operator still has on disk — a removed
// family silently degrading into a registered one.
func TestCollectorAdd_RefusesARetiredBuiltinGraphTypeName(t *testing.T) {
	userPath := useTempHome(t)
	useScratchCwd(t)

	var out bytes.Buffer
	err := runCollectorAdd([]string{"--tool", "collect", "transformers", "--", "/bin/true"}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transformers")
	assert.Contains(t, err.Error(), "RETIRED")

	_, statErr := os.Stat(userPath)
	assert.True(t, os.IsNotExist(statErr), "a refused name must not have created the file")
}

// TestCollectorScopes_DefaultIsUserAndProjectIsExplicit pins the default scope
// and the project scope's location, by the file each write lands in.
func TestCollectorScopes_DefaultIsUserAndProjectIsExplicit(t *testing.T) {
	userPath := useTempHome(t)
	cwd := useScratchCwd(t)
	url := startContractProvider(t, true)

	var out bytes.Buffer
	require.NoError(t, runCollectorAdd([]string{"-t", "http", "--tool", "collect", "in-user", url}, &out))
	require.FileExists(t, userPath, "with no -s the entry must land in the USER scope")

	require.NoError(t, runCollectorAdd([]string{"-s", "project", "-t", "http", "--tool", "collect", "in-project", url}, &out))
	projectPath := collectorconfig.ProjectPathIn(cwd)
	require.FileExists(t, projectPath, "-s project must land under <cwd>/.knowledge/")

	userEntries, err := collectorconfig.ReadRaw(userPath)
	require.NoError(t, err)
	_, leaked := userEntries["in-project"]
	assert.False(t, leaked, "the project entry must not also be in the user file")
}
