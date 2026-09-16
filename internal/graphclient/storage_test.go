// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestStorageBackendRetainsAccount pins what a BOUND backend does when the user
// switches accounts underneath it: it keeps serving the account it was bound to
// and keeps STAMPING that account, so an operation that started for one account
// finishes there instead of half-landing in another.
//
// It used to refuse instead ("bound cloud account changed"), because the
// interceptor compared the binding with the live selection. The binding is now
// the SOURCE of the stamp: a request may name its own account, and comparing it
// to the process selection is exactly what made that impossible. The refusal
// this test asserted was never the protection — the user is still a member of
// the account the operation began in, and membership is the gateway's call on
// every request either way.
func TestStorageBackendRetainsAccount(t *testing.T) {
	const bound = "11111111-1111-4111-8111-111111111111"
	const switched = "22222222-2222-4222-8222-222222222222"
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, bound); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, time.Nanosecond)))
	cloudURL, cloud := startAccountRoutedEngine(t)
	r := NewRouterWithMachineAuth(nil, cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	ctx := WithOperation(t.Context(), OperationForTool("query"))
	backend, err := r.Backend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteSelectedAccountID(path, switched); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Execute(ctx, &knowledgev1.ExecuteRequest{}); err != nil {
		t.Fatalf("the bound backend refused after an account switch: %v", err)
	}
	if got := cloud.executesFor(bound); got != 1 {
		t.Fatalf("executes stamped with the bound account = %d, want 1", got)
	}
	if got := cloud.executesFor(switched); got != 0 {
		t.Fatalf("the bound backend followed the account switch: %d executes stamped with the new selection", got)
	}
}

func TestStorageMCPBoundary(t *testing.T) {
	gc := newForwarderHarness(t)
	r := NewRouterWithMachineAuth(gc, "http://127.0.0.1:1", auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	var got Destination
	m := NewMCPClient(MCPClientConfig{Client: gc, BindStorage: r.BindStorage, InterceptChain: func(ctx context.Context, p kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
		got, _ = StorageDestination(ctx)
		return p, true, kgtools.TextResult("bound")
	}})
	m.handleMCPToolCall(toolCallReq(t, "query", map[string]any{"storage": "local", "id": "same"}))
	if got.Storage != "local" {
		t.Fatalf("interceptor destination=%+v; want local", got)
	}
}

func TestStorageLocalOverride(t *testing.T) {
	localURL, local := startCountingEngine(t)
	cloudURL, cloud := startCountingEngine(t)
	r := NewRouterWithMachineAuth(NewGraphClientForURL(localURL), cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	ctx, err := r.BindStorage(WithOperation(t.Context(), OperationForTool("query")), "local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{}); err != nil {
		t.Fatal(err)
	}
	if local.execute.Load() != 1 || cloud.execute.Load() != 0 {
		t.Fatalf("local=%d cloud=%d; want 1,0", local.execute.Load(), cloud.execute.Load())
	}
	if _, err := r.BindStorage(t.Context(), "invalid"); err == nil {
		t.Fatal("invalid storage accepted")
	}
}

func TestStorageReferenceVersions(t *testing.T) {
	const previous = `kgref:1:{"storage":"cloud","account":"account-a","graph":"code","repo":"a/b","branch":"topic","id":"文件:a/b"}`
	ref, qualified, err := ParseReference(previous)
	if err != nil || !qualified {
		t.Fatalf("ParseReference: qualified=%v err=%v", qualified, err)
	}
	if ref.AccountID != "account-a" || ref.ID != "文件:a/b" || ref.Repo != "a/b" {
		t.Fatalf("reference=%+v", ref)
	}
	encoded, err := ref.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := ParseReference(encoded)
	if err != nil || decoded != ref {
		t.Fatalf("roundtrip=%+v err=%v", decoded, err)
	}
	if _, _, err := ParseReference(`kgref:99:{}`); err == nil {
		t.Fatal("future version accepted")
	}
}

func TestStorageQualifiedRead(t *testing.T) {
	localURL, local := startCountingEngine(t)
	cloudURL, cloud := startCountingEngine(t)
	r := NewRouterWithMachineAuth(NewGraphClientForURL(localURL), cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	ctx := WithOperation(t.Context(), OperationForTool("query"))
	const ref = `kgref:2:{"storage":"local","graph":"knowledge","id":"same"}`
	request := &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: ref}}}
	if _, err := r.Execute(ctx, request); err != nil {
		t.Fatal(err)
	}
	if local.execute.Load() != 1 || cloud.execute.Load() != 0 {
		t.Fatalf("local=%d cloud=%d; want 1,0", local.execute.Load(), cloud.execute.Load())
	}
	if request.GetQuery().ById != ref {
		t.Fatal("caller request was modified")
	}
}

func TestStorageMixedHydrationRoutesBothCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
	localURL, local := startCountingEngine(t)
	cloudURL, cloud := startCountingEngine(t)
	r := NewRouterWithMachineAuth(NewGraphClientForURL(localURL), cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	ctx := WithOperation(t.Context(), OperationForTool("query"))
	request := &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: []string{
		`kgref:2:{"storage":"local","graph":"knowledge","id":"same"}`,
		`kgref:2:{"storage":"cloud","account":"a","graph":"knowledge","id":"same"}`,
	}}}}
	if _, err := r.Execute(ctx, request); err != nil {
		t.Fatal(err)
	}
	if local.execute.Load() != 1 || cloud.execute.Load() != 1 {
		t.Fatalf("hydration requests local=%d cloud=%d; want 1,1", local.execute.Load(), cloud.execute.Load())
	}
}

func TestStorageMixedPlainAndQualifiedRead(t *testing.T) {
	localURL, local := startCountingEngine(t)
	r := NewRouter(NewGraphClientForURL(localURL), "http://127.0.0.1:1", auth.StaticTokenSource{}, nil)
	t.Cleanup(r.Close)
	ctx := WithOperation(t.Context(), OperationForTool("query"))
	_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: []string{"plain", `kgref:2:{"storage":"local","graph":"knowledge","id":"qualified"}`}}}})
	if err != nil {
		t.Fatal(err)
	}
	if local.execute.Load() != 1 {
		t.Fatalf("requests=%d", local.execute.Load())
	}
}

func TestStorageCloudLocalLinkRefusedBeforeWrite(t *testing.T) {
	localURL, local := startCountingEngine(t)
	cloudURL, cloud := startCountingEngine(t)
	r := NewRouterWithMachineAuth(NewGraphClientForURL(localURL), cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	ctx := WithDestination(WithOperation(t.Context(), OperationForTool("mutate")), Destination{Storage: "cloud", AccountID: "a"})
	request := &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
		Kind:      knowledgev1.MutationPlan_MUTATION_KIND_LINK,
		Selection: &knowledgev1.Selection{Ids: []string{"source"}},
		EdgeSpec:  &knowledgev1.EdgeSpec{ToId: `kgref:2:{"storage":"local","graph":"knowledge","id":"target"}`, Relationship: "relates-to", Forward: true},
	}}}
	if _, err := r.Execute(ctx, request); err == nil {
		t.Fatal("cloud-to-local link accepted")
	}
	if local.execute.Load() != 0 || cloud.execute.Load() != 0 {
		t.Fatalf("attempted forbidden write: local=%d cloud=%d", local.execute.Load(), cloud.execute.Load())
	}
}

func TestStorageBatchRejectsLocalEdgeBeforeCreate(t *testing.T) {
	ctx := WithDestination(t.Context(), Destination{Storage: "cloud", AccountID: "a"})
	request := &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
		Kind:       knowledgev1.MutationPlan_MUTATION_KIND_CREATE,
		NodeBodies: []*knowledgev1.NodeBody{{Type: "finding", Name: "new node", Summary: "new node"}},
		Edges:      []*knowledgev1.BatchEdgeSpec{{FromIdx: 0, ToIdx: -1, ToId: `kgref:2:{"storage":"local","graph":"knowledge","id":"target"}`, Type: "relates-to"}},
	}}}
	if _, _, err := resolveRequestReferences(ctx, request); err == nil {
		t.Fatal("cloud create with local batch edge accepted")
	}
}

func TestStorageRejectsProxyRetargetBeforeWrite(t *testing.T) {
	ref := `kgref:2:{"storage":"local","graph":"knowledge","id":"target"}`
	for name, mutation := range map[string]*knowledgev1.MutationPlan{
		"items":  {Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, UpdateItems: []*knowledgev1.UpdateItem{{Id: "proxy", Metadata: map[string]string{"storage_reference": ref}}}},
		"upsert": {Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, NodeBodies: []*knowledgev1.NodeBody{{Id: "proxy", Type: "proxy", Metadata: map[string]string{"storage_reference": ref}}}},
		"update": {Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, Selection: &knowledgev1.Selection{Ids: []string{"proxy"}}, SetMetadata: map[string]string{"storage_reference": ref}},
		"source": {Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, Selection: &knowledgev1.Selection{Ids: []string{"proxy"}}, SetFields: map[string]string{"source": ref}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := WithDestination(t.Context(), Destination{Storage: "cloud", AccountID: "a"})
			_, _, err := resolveRequestReferences(ctx, &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: mutation}})
			if err == nil {
				t.Fatal("cloud proxy retargeted to local")
			}
		})
	}
}

func TestStorageFederationRequiresCloudEvenWithoutHits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
	r := NewRouterWithMachineAuth(NewGraphClientForURL("http://127.0.0.1:1"), "http://127.0.0.1:1", auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	ctx, err := r.BindStorage(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.BindSearch(ctx)
	if err == nil {
		t.Fatal("unavailable cloud accepted as complete search")
	}
	// Both legs are dead here, so WHICH store the message names is the whole
	// content of the answer: the REQUIRED cloud store is what stopped the
	// search, and naming the optional local one is the confusion this row
	// exists to keep out.
	if !strings.Contains(err.Error(), "cloud storage unavailable") {
		t.Fatalf("both stores down: error = %v, want it to name the REQUIRED cloud store, not the optional local one", err)
	}
}

func TestStorageSearchCatalogReadsBothBackends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
	localURL, local := startCountingEngine(t)
	cloudURL, cloud := startCountingEngine(t)
	r := NewRouterWithMachineAuth(NewGraphClientForURL(localURL), cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	ctx := WithSearchDestinations(WithOperation(t.Context(), OperationForTool("search")), []Destination{{Storage: "local"}, {Storage: "cloud", AccountID: "a"}})
	_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{Target: &knowledgev1.GraphSelector{Graph: "code"}, Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES}}})
	if err != nil {
		t.Fatal(err)
	}
	if local.execute.Load() != 1 || cloud.execute.Load() != 1 {
		t.Fatalf("local=%d cloud=%d", local.execute.Load(), cloud.execute.Load())
	}
}

func TestStorageBatchSourceReferencesValidatedBeforeMetadata(t *testing.T) {
	ctx := WithDestination(t.Context(), Destination{Storage: "local"})
	cloud := `kgref:2:{"storage":"cloud","account":"a","graph":"knowledge","id":"source"}`
	local := `kgref:2:{"storage":"local","graph":"knowledge","id":"target"}`
	m := &knowledgev1.MutationPlan{Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, NodeBodies: []*knowledgev1.NodeBody{{Id: cloud, Metadata: map[string]string{"storage_reference": local}}}}
	if _, _, err := resolveRequestReferences(ctx, &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: m}}); err == nil {
		t.Fatal("qualified cloud body accepted local reference")
	}
	m.NodeBodies[0].Id = ""
	m.Edges = []*knowledgev1.BatchEdgeSpec{{FromId: cloud, ToId: "target"}}
	if _, _, err := resolveRequestReferences(ctx, &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: m}}); err == nil {
		t.Fatal("qualified cloud edge source bypassed metadata preflight")
	}
}

func TestStorageMCPReferenceSiblingTargets(t *testing.T) {
	localURL, _ := startCountingEngine(t)
	r := NewRouterWithMachineAuth(NewGraphClientForURL(localURL), "http://127.0.0.1:1", auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	m := NewMCPClient(MCPClientConfig{BindStorage: r.BindStorage})
	for _, key := range []string{"thought", "thought_id"} {
		t.Run(key, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{"operation": "charge", key: `kgref:2:{"storage":"local","graph":"knowledge","id":"same"}`})
			if err != nil {
				t.Fatal(err)
			}
			ctx, _, err := m.prepareStorageCall(t.Context(), kgtools.CallToolParams{Name: "thoughts", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			d, _ := StorageDestination(ctx)
			if d.Storage != "local" {
				t.Fatalf("destination=%+v", d)
			}
		})
	}
	ctx, _, err := m.prepareStorageCall(t.Context(), kgtools.CallToolParams{Name: "search", Arguments: json.RawMessage(`{"storage":"local","query":"needle"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(SearchDestinations(ctx)) != 1 {
		t.Fatal("explicit search cannot emit destination-qualified results")
	}
}

func TestStorageQualifiedResponseCarriers(t *testing.T) {
	const ref = `kgref:2:{"storage":"local","graph":"knowledge","id":"source"}`
	for _, selection := range []bool{false, true} {
		t.Run(fmt.Sprint(selection), func(t *testing.T) {
			gc := newEngineHarness(t, func(_ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
				return &knowledgev1.ExecuteResponse{Nodes: []*knowledgev1.Node{{Id: "peer"}}, Ids: []string{"peer"}, Edges: []*knowledgev1.Edge{{FromId: "source", ToId: "peer"}}, TraversalResults: []*knowledgev1.TraversalResult{{Node: &knowledgev1.Node{Id: "peer"}}}, TraversalEdges: []*knowledgev1.Edge{{FromId: "source", ToId: "peer"}}}, nil
			})
			r := NewRouter(gc, "", auth.StaticTokenSource{}, nil)
			t.Cleanup(r.Close)
			q := &knowledgev1.QueryPlan{ById: ref}
			if selection {
				q.ById = ""
				q.Selection = &knowledgev1.Selection{FromId: []string{ref}}
			}
			response, err := r.Execute(opCtx(), &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Query{Query: q}})
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{response.Nodes[0].Id, response.Ids[0], response.Edges[0].FromId, response.Edges[0].ToId, response.TraversalResults[0].Node.Id, response.TraversalEdges[0].ToId} {
				parsed, qualified, err := ParseReference(id)
				if err != nil || !qualified || parsed.Storage != "local" {
					t.Fatalf("unqualified result carrier %q err=%v", id, err)
				}
			}
		})
	}
}

func TestStorageMCPRequiresConcreteCloudAccount(t *testing.T) {
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(filepath.Join(t.TempDir(), "config"), 0)))
	r := NewRouterWithMachineAuth(nil, "http://127.0.0.1:1", auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	m := NewMCPClient(MCPClientConfig{BindStorage: r.BindStorage})
	_, _, err := m.prepareStorageCall(t.Context(), kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"id":"same"}`)})
	if err == nil || !strings.Contains(err.Error(), "knowledge account use") {
		t.Fatalf("missing selected account accepted or unactionable: %v", err)
	}
}

func TestStorageBatchIndicesRetainEndpointIdentity(t *testing.T) {
	ctx := WithDestination(t.Context(), Destination{Storage: "local"})
	m := &knowledgev1.MutationPlan{Kind: knowledgev1.MutationPlan_MUTATION_KIND_CREATE, NodeBodies: []*knowledgev1.NodeBody{{Type: "proxy", Metadata: map[string]string{"storage_reference": `kgref:2:{"storage":"cloud","account":"a","graph":"knowledge","id":"cloud"}`}}, {Type: "finding"}}, Edges: []*knowledgev1.BatchEdgeSpec{{FromIdx: 0, ToIdx: 1}}}
	if err := (*Router)(nil).validateStorageRelationships(ctx, &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: m}}); err == nil {
		t.Fatal("empty generated IDs collapsed batch endpoint identities")
	}
}

func TestStorageMixedMutationIDsRouteBothCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
	localURL, local := startCountingEngine(t)
	cloudURL, cloud := startCountingEngine(t)
	r := NewRouterWithMachineAuth(NewGraphClientForURL(localURL), cloudURL, auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	ctx := WithDestination(WithOperation(t.Context(), OperationForTool("mutate")), Destination{Storage: "cloud", AccountID: "a"})
	_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, Selection: &knowledgev1.Selection{Ids: []string{`kgref:2:{"storage":"local","graph":"knowledge","id":"same"}`, `kgref:2:{"storage":"cloud","account":"a","graph":"knowledge","id":"same"}`}}, SetFields: map[string]string{"description": "body"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if local.execute.Load() != 1 || cloud.execute.Load() != 1 {
		t.Fatalf("local=%d cloud=%d", local.execute.Load(), cloud.execute.Load())
	}
}

func TestStorageSerializedReferencesPreflight(t *testing.T) {
	encoded, err := json.Marshal([]string{`kgref:2:{"storage":"local","graph":"knowledge","id":"target"}`})
	if err != nil {
		t.Fatal(err)
	}
	d := Destination{Storage: "cloud", AccountID: "a"}
	if err := validateArgumentReferences(string(encoded), d, "mutate"); err == nil {
		t.Fatal("serialized local reference bypassed MCP preflight")
	}
	_, _, err = resolveRequestReferences(WithDestination(t.Context(), d), &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, SetMetadata: map[string]string{"links": string(encoded)}}}})
	if err == nil {
		t.Fatal("serialized local reference bypassed wire preflight")
	}
}

func TestStorageLocalReferenceListNeedsNoCloudAccount(t *testing.T) {
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(filepath.Join(t.TempDir(), "config"), 0)))
	r := NewRouterWithMachineAuth(NewGraphClientForURL("http://127.0.0.1:1"), "http://127.0.0.1:1", auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	m := NewMCPClient(MCPClientConfig{BindStorage: r.BindStorage})
	args, marshalErr := json.Marshal(map[string]any{"operation": "delete", "ids": []string{`kgref:2:{"storage":"local","graph":"knowledge","id":"same"}`}})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	ctx, _, err := m.prepareStorageCall(t.Context(), kgtools.CallToolParams{Name: "mutate", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := StorageDestination(ctx)
	if d.Storage != "local" {
		t.Fatalf("destination=%+v", d)
	}
}

func TestStorageInventoryContextsKeepPlacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := config.WriteSelectedAccountID(path, "a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
	r := NewRouterWithMachineAuth(NewGraphClientForURL("http://127.0.0.1:1"), "http://127.0.0.1:1", auth.StaticTokenSource{}, nil, true)
	t.Cleanup(r.Close)
	contexts, err := r.InventoryContexts(WithSearchDestinations(t.Context(), []Destination{{Storage: "local"}, {Storage: "cloud", AccountID: "a"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 2 {
		t.Fatalf("contexts=%d", len(contexts))
	}
	for i, ctx := range contexts {
		storage, account, err := r.InventoryPlacement(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := []Destination{{Storage: "local"}, {Storage: "cloud", AccountID: "a"}}[i]
		if storage != want.Storage || account != want.AccountID || len(SearchDestinations(ctx)) != 0 {
			t.Fatalf("placement=%s/%s search=%v", storage, account, SearchDestinations(ctx))
		}
	}
}
