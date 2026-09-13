// SPDX-License-Identifier: Apache-2.0

//go:build storageintegration

package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

func TestStorageLive(t *testing.T) {
	localURL, cloudURL := os.Getenv("KNOWLEDGE_STORAGE_TEST_LOCAL"), os.Getenv("KNOWLEDGE_STORAGE_TEST_CLOUD")
	require.NotEmpty(t, localURL, "run through server-bench TestSplitStorageClient")
	require.NotEmpty(t, cloudURL)
	account := os.Getenv("KNOWLEDGE_STORAGE_TEST_ACCOUNT")
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, config.WriteSelectedAccountID(path, account))
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
	// routing: routed — the fixture client is the local arm of the mixed Router below.
	local := graphclient.NewGraphClientForURL(localURL)
	credentials := newFakeAuthStore()
	require.NoError(t, credentials.Set(t.Context(), auth.KeyRefreshToken, "fixture-refresh"))
	require.NoError(t, credentials.Set(t.Context(), auth.KeyAccessToken, os.Getenv("KNOWLEDGE_STORAGE_TEST_TOKEN")))
	require.NoError(t, credentials.Set(t.Context(), auth.KeyAccessTokenExpiry, time.Now().Add(time.Hour).Format(time.RFC3339)))
	r := graphclient.NewRouter(local, cloudURL, auth.NewReadOnlyTokenSource(credentials), auth.NewAuthState(credentials, time.Nanosecond))
	t.Cleanup(r.Close)
	mgr := segmentdist.NewManager(t.TempDir(), 0)
	t.Cleanup(func() { mgr.Close() })
	c := &client{local: local, router: r, rootDir: t.TempDir(), workingSet: workingset.New(), segmentMgr: mgr}
	// The search index is populated below through the real segment manager;
	// provider enrichment is outside this routing/search scenario.
	c.markPipelineReady()
	r.AttachDestinationWorkingSet(c.AdmitDestinationGraph)
	destinations := []graphclient.Destination{{Storage: "local"}, {Storage: "cloud", AccountID: account}}
	for _, d := range destinations {
		nodeType := "finding"
		if d.Storage == "cloud" {
			nodeType = "research"
		}
		ctx := graphclient.WithDestination(graphclient.WithOperation(t.Context(), graphclient.OperationForTool("mutate")), d)
		_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{Target: &knowledgev1.GraphSelector{Graph: "knowledge"}, Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, NodeBodies: []*knowledgev1.NodeBody{{Id: "storage-live-same", Type: nodeType, Name: d.Storage + " copy", Summary: d.Storage + " needle", Description: d.Storage + " storage body"}},
		}}})
		require.NoError(t, err)
		require.NoError(t, mgr.ReplaceBucketFields(ctx, kgtypes.GraphKnowledge, "default", nil, []searchengine.Document{{ID: "storage-live-same", Fields: map[string]string{searchengine.FieldContent: "needle"}}}))
		_, err = r.Execute(ctx, &knowledgev1.ExecuteRequest{Target: &knowledgev1.GraphSelector{Graph: "practice"}, Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
			Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, NodeBodies: []*knowledgev1.NodeBody{{Id: "practice-same", Type: "pattern", Name: d.Storage + " practice", Summary: d.Storage + " needle", Description: "practice body", Metadata: map[string]string{kgtypes.MetaKeySourceHub: d.Storage + "-hub"}}},
		}}})
		require.NoError(t, err)
		require.NoError(t, mgr.ReplaceBucketFields(ctx, kgtypes.GraphPractice, "default", nil, []searchengine.Document{{ID: "practice-same", Fields: map[string]string{searchengine.FieldContent: "needle"}}}))
	}
	mc := graphclient.NewMCPClient(graphclient.MCPClientConfig{Client: local, BindStorage: r.BindStorage, BindSearch: r.BindSearch, LoggedIn: r.LoggedIn, Dispatch: c.engineDispatch, InterceptChain: c.runInterceptChain})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- graphclient.NewHTTPServer(mc, port, nil).Run(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp"
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: transport}
	var session string
	request := func(method string, params any) ([]byte, error) {
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Session-Id", session)
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if value := resp.Header.Get("Mcp-Session-Id"); value != "" {
			session = value
		}
		return io.ReadAll(resp.Body)
	}
	require.Eventually(t, func() bool {
		_, err := request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "storage-test", "version": "1"}})
		return err == nil && session != ""
	}, 5*time.Second, 10*time.Millisecond)
	call := func(tool string, args map[string]any) string {
		b, err := request("tools/call", map[string]any{"name": tool, "arguments": args})
		require.NoError(t, err)
		require.NotContains(t, string(b), `"isError":true`)
		return string(b)
	}
	if script := os.Getenv("DESKTOP_STORAGE_UI"); script != "" {
		require.NotEmpty(t, os.Getenv("DESKTOP_STORAGE_UI_DIGEST"), "run UI through TestSplitStorageClient")
		binary, err := os.Executable()
		require.NoError(t, err)
		built, err := os.ReadFile(binary)
		require.NoError(t, err)
		t.Logf("mixed MCP test binary sha256=%x; UI inputs sha256=%s", sha256.Sum256(built), os.Getenv("DESKTOP_STORAGE_UI_DIGEST"))
		out, err := os.Create(filepath.Join(t.TempDir(), "desktop-storage.log"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = out.Close() })
		ui := exec.CommandContext(ctx, "node", script) // #nosec G702 -- The trusted test launcher explicitly selects the UI program, not an MCP request.
		ui.Env = append(os.Environ(), "DESKTOP_STORAGE_MCP_PORT="+strconv.Itoa(port))
		ui.Stdout, ui.Stderr = out, out
		err = ui.Run()
		body, readErr := os.ReadFile(out.Name())
		require.NoError(t, readErr)
		t.Log(string(body))
		require.NoError(t, err)
	}
	require.Contains(t, call("query", map[string]any{"graph": "knowledge", "id": "storage-live-same", "storage": "local"}), "local storage body")
	require.Contains(t, call("query", map[string]any{"graph": "knowledge", "id": "storage-live-same"}), "cloud storage body")
	result := call("search", map[string]any{"graph": "knowledge", "query": "needle"})
	require.Contains(t, result, "local needle")
	require.Contains(t, result, "cloud needle")
	require.Contains(t, result, "kgref:2:")
	filtered := call("search", map[string]any{"graph": "knowledge", "query": "needle", "types": []string{"finding"}})
	require.Contains(t, filtered, "local needle")
	require.NotContains(t, filtered, "cloud needle")
	hub := call("search", map[string]any{"graph": "practice", "query": "needle", "source_hub": "local-hub"})
	require.Contains(t, hub, "local practice")
	require.NotContains(t, hub, "cloud practice")
	localRef, err := (graphclient.Reference{Destination: destinations[0], Graph: "knowledge", ID: "storage-live-same"}).Encode()
	require.NoError(t, err)
	cloudRef, err := (graphclient.Reference{Destination: destinations[1], Graph: "knowledge", ID: "storage-live-same"}).Encode()
	require.NoError(t, err)
	require.Contains(t, call("query", map[string]any{"id": localRef}), "local storage body")
	require.Contains(t, call("query", map[string]any{"id": cloudRef}), "cloud storage body")
	call("mutate", map[string]any{"operation": "link", "from": localRef, "to": cloudRef, "relationship": "relates-to"})
	call("mutate", map[string]any{"operation": "update", "id": cloudRef, "description": "updated cloud body"})
	proxyID := fmt.Sprintf("storage-proxy:%x", sha256.Sum256([]byte(cloudRef)))
	require.Contains(t, call("query", map[string]any{"id": proxyID, "storage": "local"}), "updated cloud body")
	require.Contains(t, call("traverse", map[string]any{"start": localRef, "direction": "out", "edge_types": []string{"relates-to"}, "format": "json"}), "updated cloud body")
	reused, reuseErr := request("tools/call", map[string]any{"name": "mutate", "arguments": map[string]any{"operation": "link", "storage": "local", "from": proxyID, "to": localRef, "relationship": "relates-to"}})
	require.NoError(t, reuseErr)
	require.Contains(t, string(reused), `"isError":true`, "a local proxy of a cloud source cannot link back to local")
	require.Contains(t, string(reused), "cloud-to-local")
	// A reused cloud proxy must be rejected before creating a
	// second proxy for a local endpoint in another graph.
	practiceRef, err := (graphclient.Reference{Destination: destinations[0], Graph: "practice", ID: "practice-same"}).Encode()
	require.NoError(t, err)
	practiceProxy := fmt.Sprintf("storage-proxy:%x", sha256.Sum256([]byte(practiceRef)))
	proxyRef, err := (graphclient.Reference{Destination: destinations[0], Graph: "knowledge", ID: proxyID}).Encode()
	require.NoError(t, err)

	before, err := local.Execute(graphclient.WithDestination(graphclient.WithOperation(t.Context(), graphclient.OperationForTool("query")), destinations[0]), &knowledgev1.ExecuteRequest{Target: &knowledgev1.GraphSelector{Graph: "knowledge"}, Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: []string{practiceProxy}}}})
	require.NoError(t, err)
	require.Empty(t, before.Nodes, "precondition: target proxy was absent")
	forbidden, err := request("tools/call", map[string]any{"name": "mutate", "arguments": map[string]any{"operation": "link", "storage": "local", "from": proxyRef, "to": practiceRef, "link_graph": "linkage", "relationship": "relates-to"}})
	require.NoError(t, err)
	require.Contains(t, string(forbidden), `"isError":true`)
	residue, err := local.Execute(graphclient.WithDestination(graphclient.WithOperation(t.Context(), graphclient.OperationForTool("query")), destinations[0]), &knowledgev1.ExecuteRequest{Target: &knowledgev1.GraphSelector{Graph: "knowledge"}, Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: []string{practiceProxy}}}})
	require.NoError(t, err)
	require.Empty(t, residue.Nodes, "forbidden reused-cloud-proxy to local reference attempted a proxy write before refusal")
	// The same composer still permits local/local, local/cloud and cloud/cloud.
	call("mutate", map[string]any{"operation": "link", "from": localRef, "to": practiceRef, "link_graph": "linkage", "relationship": "relates-to"})
	call("mutate", map[string]any{"operation": "link", "from": localRef, "to": cloudRef, "link_graph": "linkage", "relationship": "relates-to"})
	cloudPractice, err := (graphclient.Reference{Destination: destinations[1], Graph: "practice", ID: "practice-same"}).Encode()
	require.NoError(t, err)
	call("mutate", map[string]any{"operation": "link", "from": cloudRef, "to": cloudPractice, "link_graph": "linkage", "relationship": "relates-to"})
	call("mutate", map[string]any{"operation": "link", "from": cloudRef, "to": cloudPractice, "relationship": "relates-to"})

	b, err := request("tools/call", map[string]any{"name": "mutate", "arguments": map[string]any{"operation": "link", "from": cloudRef, "to": localRef, "relationship": "relates-to"}})
	require.NoError(t, err)
	require.Contains(t, string(b), `"isError":true`)
	require.Contains(t, string(b), "cloud-to-local")
	call("mutate", map[string]any{"operation": "update_batch", "items": []map[string]any{{"id": localRef, "description": "local batch body"}, {"id": cloudRef, "description": "cloud batch body"}}})
	require.Contains(t, call("query", map[string]any{"id": localRef}), "local batch body")
	require.Contains(t, call("query", map[string]any{"id": cloudRef}), "cloud batch body")
	encodedLinks, err := json.Marshal([]string{localRef})
	require.NoError(t, err)
	badBatch, err := request("tools/call", map[string]any{"name": "mutate", "arguments": map[string]any{"operation": "update_batch", "items": []map[string]any{{"id": localRef, "description": "must not land"}, {"id": cloudRef, "metadata": map[string]string{"links": string(encodedLinks)}}}}})
	require.NoError(t, err)
	require.Contains(t, string(badBatch), `"isError":true`)
	require.Contains(t, string(badBatch), "cloud-to-local")
	require.Contains(t, call("query", map[string]any{"id": localRef}), "local batch body")
	localCtx := graphclient.WithDestination(graphclient.WithOperation(t.Context(), graphclient.OperationForTool("mutate")), destinations[0])
	_, err = r.Execute(localCtx, &knowledgev1.ExecuteRequest{Target: &knowledgev1.GraphSelector{Graph: "knowledge"}, Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, NodeBodies: []*knowledgev1.NodeBody{{Id: "retarget-peer", Type: "finding", Name: "local peer", Summary: "local peer"}}}}})
	require.NoError(t, err)
	call("mutate", map[string]any{"operation": "link", "from": localRef, "to": "retarget-peer", "relationship": "relates-to"})
	edges, err := local.Execute(localCtx, &knowledgev1.ExecuteRequest{Target: &knowledgev1.GraphSelector{Graph: "knowledge"}, Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: "storage-live-same", ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES}}})
	require.NoError(t, err)
	require.NotEmpty(t, edges.Edges, "retarget has existing edges")
	for _, operation := range []string{"update", "update_batch"} {
		args := map[string]any{"operation": operation, "id": localRef, "metadata": map[string]string{"storage_reference": cloudRef}}
		if operation == "update_batch" {
			args = map[string]any{"operation": operation, "items": []map[string]any{{"id": localRef, "metadata": map[string]string{"storage_reference": cloudRef}}}}
		}
		retarget, err := request("tools/call", map[string]any{"name": "mutate", "arguments": args})
		require.NoError(t, err)
		require.Contains(t, string(retarget), `"isError":true`, "retarget must validate existing local edges: %s", operation)
		require.Contains(t, call("query", map[string]any{"id": localRef}), "local batch body")
	}

	// The same running MCP remains usable locally as its credential store
	// transitions to signed-out, then back to the same cloud identity.
	for _, key := range []string{auth.KeyRefreshToken, auth.KeyAccessToken, auth.KeyAccessTokenExpiry} {
		require.NoError(t, credentials.Delete(ctx, key))
	}
	require.False(t, r.LoggedIn(ctx))
	require.Contains(t, call("query", map[string]any{"graph": "knowledge", "id": "storage-live-same"}), "local batch body")
	require.Contains(t, call("query", map[string]any{"graph": "knowledge", "id": "storage-live-same", "storage": "local"}), "local batch body")
	require.NoError(t, credentials.Set(ctx, auth.KeyRefreshToken, "fixture-refresh"))
	require.NoError(t, credentials.Set(ctx, auth.KeyAccessToken, os.Getenv("KNOWLEDGE_STORAGE_TEST_TOKEN")))
	require.NoError(t, credentials.Set(ctx, auth.KeyAccessTokenExpiry, time.Now().Add(time.Hour).Format(time.RFC3339)))
	require.Contains(t, call("query", map[string]any{"graph": "knowledge", "id": "storage-live-same"}), "cloud batch body")
	require.Contains(t, call("query", map[string]any{"graph": "knowledge", "id": "storage-live-same", "storage": "local"}), "local batch body")
}
