// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeBackendStore is an in-memory log_backend store wrapped behind a
// fakeGraphCaller. The client-side configure/list handlers issue
// mutate(upsert, type:"log-backend") and query(type:"log-backend",
// format:"json") RPCs against the gc; this fake answers those two RPC
// shapes against a simple keyed map. Production hits the server's
// generic mutate/query handlers — but the wire shape is identical, so
// asserting against this fake is equivalent to an integration test
// minus the on-disk store.
type fakeBackendStore struct {
	records map[string]logBackendRecord
	// queryErr/upsertErr force the next matching call to surface an
	// error. Cleared after one call; nil = succeed normally.
	queryErr  error
	upsertErr error
	// calls accumulates the (tool, args) tuples so tests can assert on
	// wire shape.
	calls []recordedCall
}

func newFakeBackendStore() *fakeBackendStore {
	return &fakeBackendStore{records: map[string]logBackendRecord{}}
}

// Call satisfies the interface; the backend handlers route through Execute.
func (f *fakeBackendStore) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

// Execute dispatches the configure/list handlers' UPSERT and type-browse reads
// over the Execute carrier seam against the in-memory map, recording a
// reconstructed (tool, args) tuple for the wire-shape assertions.
func (f *fakeBackendStore) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		return f.execUpsert(m)
	}
	return f.execQuery()
}

func (f *fakeBackendStore) execQuery() (*knowledgev1.ExecuteResponse, error) {
	f.calls = append(f.calls, recordedCall{tool: "query", args: json.RawMessage(`{"type":"log-backend"}`)})
	if f.queryErr != nil {
		err := f.queryErr
		f.queryErr = nil
		return nil, err
	}
	nodes := make([]knowledgev1.Node, 0, len(f.records))
	for _, r := range f.records {
		nodes = append(nodes, recordToStoreNode(r))
	}
	resp := enginetest.ResponseWithNodes(nodePtrs(nodes)...)
	resp.Total = int64(len(nodes))
	return resp, nil
}

func (f *fakeBackendStore) execUpsert(m *knowledgev1.MutationPlan) (*knowledgev1.ExecuteResponse, error) {
	body := m.GetNodeBodies()[0]
	// Reconstruct the upsert wire-shape for the test's type-canonical assertion.
	argsJSON, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Type      string `json:"type"`
	}{Operation: "upsert", Type: body.GetType()})
	if err != nil {
		return nil, err
	}
	f.calls = append(f.calls, recordedCall{tool: "mutate", args: argsJSON})
	if f.upsertErr != nil {
		err := f.upsertErr
		f.upsertErr = nil
		return nil, err
	}
	md := body.GetMetadata()
	f.records[body.GetId()] = logBackendRecord{
		id:         body.GetId(),
		symbolName: body.GetName(),
		provider:   md["provider"],
		url:        md["url"],
		authType:   md["auth_type"],
		credential: md["credential"],
		kubeCtx:    md["kube_context"],
	}
	return &knowledgev1.ExecuteResponse{Ids: []string{body.GetId()}}, nil
}

// recordToStoreNode emits the knowledgev1.Node the nodes_json carrier carries (the
// shape engine.DecodeNodes reads). The metadata fields ride node Metadata.
func recordToStoreNode(r logBackendRecord) knowledgev1.Node {
	return knowledgev1.Node{
		Id:         r.id,
		SymbolName: r.symbolName,
		Type:       string(kgtypes.NodeLogBackend),
		Metadata: map[string]string{
			"provider":     r.provider,
			"url":          r.url,
			"auth_type":    r.authType,
			"credential":   r.credential,
			"kube_context": r.kubeCtx,
		},
	}
}

// newBackendHandler returns a Handler wired to a fresh fake backend
// store. The fake is returned alongside so tests can inspect the
// captured RPCs or pre-seed records.
func newBackendHandler(t *testing.T) (*Handler, *fakeBackendStore) {
	t.Helper()
	store := newFakeBackendStore()
	return &Handler{graphCallerOverride: store}, store
}

// validBackend returns a well-formed set of arguments so positive-path
// tests don't repeat themselves.
func validBackend(name string) manageArgs {
	return manageArgs{
		Operation: "configure_log_backend", Name: name,
		Provider: "loki", URL: "https://logs.example.com",
		AuthType: "bearer", Credential: "$LOKI_TOKEN",
	}
}

// TestConfigureLogBackend_CreatesNode exercises the happy path: a brand
// new backend record appears in the store via the upsert RPC and carries
// the full metadata contract.
func TestConfigureLogBackend_CreatesNode(t *testing.T) {
	h, fake := newBackendHandler(t)
	ctx := context.Background()

	res := h.handleConfigureLogBackend(ctx, validBackend("prod-loki"))
	require.False(t, res.IsError, "configure should succeed: %s", resultText(res))

	require.Len(t, fake.records, 1)
	got := fake.records["prod-loki"]
	assert.Equal(t, "loki", got.value("provider"))
	assert.Equal(t, "https://logs.example.com", got.value("url"))
	assert.Equal(t, "bearer", got.value("auth_type"))
	assert.Equal(t, "$LOKI_TOKEN", got.value("credential"))

	// Wire-shape assertion: the upsert call must carry type:"log-backend"
	// (canonical kgtypes.NodeLogBackend), not the legacy alias.
	var seenUpsert bool
	for _, c := range fake.calls {
		if c.tool != "mutate" {
			continue
		}
		var a struct {
			Operation string `json:"operation"`
			Type      string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(c.args, &a))
		if a.Operation == "upsert" {
			assert.Equal(t, "log-backend", a.Type, "upsert must carry canonical type")
			seenUpsert = true
		}
	}
	assert.True(t, seenUpsert, "expected at least one upsert call")
}

// TestConfigureLogBackend_UpdatesExisting confirms the second call under
// the same name overwrites the mutable fields without duplicating the
// record.
func TestConfigureLogBackend_UpdatesExisting(t *testing.T) {
	h, fake := newBackendHandler(t)
	ctx := context.Background()

	require.False(t, h.handleConfigureLogBackend(ctx, validBackend("prod")).IsError)
	updated := validBackend("prod")
	updated.URL = "https://logs-2.example.com"
	updated.AuthType = "api_key"
	updated.Credential = "$LOKI_API_KEY"
	res := h.handleConfigureLogBackend(ctx, updated)
	require.False(t, res.IsError, "second configure should succeed: %s", resultText(res))

	require.Len(t, fake.records, 1, "update must not create a second record")
	got := fake.records["prod"]
	assert.Equal(t, "https://logs-2.example.com", got.value("url"))
	assert.Equal(t, "api_key", got.value("auth_type"))
	assert.Equal(t, "$LOKI_API_KEY", got.value("credential"))
}

// TestConfigureLogBackend_AcceptsRawCredential confirms that a raw
// credential value is accepted and stored. The knowledge graph is
// encrypted at rest (AES-256-GCM, machine-bound key), so there is no
// security reason to force callers through an env var indirection.
func TestConfigureLogBackend_AcceptsRawCredential(t *testing.T) {
	h, fake := newBackendHandler(t)
	ctx := context.Background()

	args := validBackend("raw")
	args.Credential = "a-raw-bearer-token"
	res := h.handleConfigureLogBackend(ctx, args)
	require.False(t, res.IsError, "raw credential must be accepted: %s", resultText(res))

	got := fake.records["raw"]
	assert.Equal(t, "a-raw-bearer-token", got.value("credential"))

	// The tool response must NOT echo the raw value back — the redacted
	// form is what callers see in conversation context.
	assert.NotContains(t, resultText(res), "a-raw-bearer-token",
		"response must not echo the raw credential")
}

// TestConfigureLogBackend_RequiresCredential enforces the one remaining
// invariant: the credential field must be present.
func TestConfigureLogBackend_RequiresCredential(t *testing.T) {
	h, fake := newBackendHandler(t)
	ctx := context.Background()

	for _, empty := range []string{"", "   "} {
		args := validBackend("empty")
		args.Credential = empty
		res := h.handleConfigureLogBackend(ctx, args)
		require.True(t, res.IsError, "empty credential %q must be rejected", empty)
		assert.Contains(t, resultText(res), "credential")
	}

	assert.Empty(t, fake.records, "empty credential must not write a record")
}

// TestConfigureLogBackend_ProviderIsImmutable verifies the identity key
// cannot be rotated through a subsequent configure call under the same
// name.
func TestConfigureLogBackend_ProviderIsImmutable(t *testing.T) {
	h, _ := newBackendHandler(t)
	ctx := context.Background()

	require.False(t, h.handleConfigureLogBackend(ctx, validBackend("prod")).IsError)

	mutated := validBackend("prod")
	mutated.Provider = "cloudwatch"
	res := h.handleConfigureLogBackend(ctx, mutated)
	require.True(t, res.IsError, "provider must be immutable")
	assert.Contains(t, resultText(res), "provider")
}

// TestListLogBackends_SortsByName confirms the list response is
// deterministic (alphabetical by name) and includes all configured
// records.
func TestListLogBackends_SortsByName(t *testing.T) {
	h, _ := newBackendHandler(t)
	ctx := context.Background()

	names := []string{"zebra", "alpha", "middle"}
	for _, n := range names {
		require.False(t, h.handleConfigureLogBackend(ctx, validBackend(n)).IsError)
	}

	res := h.handleListLogBackends(ctx, "")
	require.False(t, res.IsError, "list should succeed: %s", resultText(res))
	txt := resultText(res)

	// Order-sensitive assertions: find indices of each row and confirm they
	// ascend in alphabetical order.
	idxAlpha := strings.Index(txt, "| alpha ")
	idxMiddle := strings.Index(txt, "| middle ")
	idxZebra := strings.Index(txt, "| zebra ")
	require.NotEqual(t, -1, idxAlpha, "alpha row missing: %s", txt)
	require.NotEqual(t, -1, idxMiddle, "middle row missing: %s", txt)
	require.NotEqual(t, -1, idxZebra, "zebra row missing: %s", txt)
	assert.Less(t, idxAlpha, idxMiddle, "alpha must come before middle")
	assert.Less(t, idxMiddle, idxZebra, "middle must come before zebra")
}

// TestListLogBackends_EmptyStateHelpful ensures the empty response isn't
// an empty table but a guide pointing at configure_log_backend.
func TestListLogBackends_EmptyStateHelpful(t *testing.T) {
	h, _ := newBackendHandler(t)
	ctx := context.Background()

	res := h.handleListLogBackends(ctx, "")
	require.False(t, res.IsError)
	assert.Contains(t, resultText(res), "No log_backend records")
	assert.Contains(t, resultText(res), "configure_log_backend")
}

// TestRedactCredential confirms that env var references pass through
// unchanged (they carry no secret) while raw values are replaced with a
// length-only placeholder for conversation safety.
func TestRedactCredential(t *testing.T) {
	assert.Equal(t, "$LOKI_TOKEN", redactCredential("$LOKI_TOKEN"))
	assert.Equal(t, "$VAR", redactCredential("  $VAR  "))
	assert.Equal(t, "[redacted]", redactCredential("abc"))
	assert.Equal(t, "[redacted 20 chars]", redactCredential("abcdefghijklmnopqrst"))
}
