// SPDX-License-Identifier: Apache-2.0
package graphclient

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

func TestStorageOptionalArgumentsReachIntercept(t *testing.T) {
	for _, arguments := range []string{"", "{}", "null", "[]", "1", `"text"`, "{} {}", " "} {
		t.Run(arguments, func(t *testing.T) {
			reached := false
			m := NewMCPClient(MCPClientConfig{BindStorage: func(ctx context.Context, _ string) (context.Context, error) {
				return WithDestination(ctx, Destination{Storage: "local"}), nil
			}, InterceptChain: func(_ context.Context, p kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
				reached = true
				return p, true, kgtools.TextResult("reached")
			}})
			params := `{"name":"help"}`
			if arguments != "" {
				params = `{"name":"help","arguments":` + arguments + `}`
			}
			m.handleMCPToolCall(kgtools.JSONRPCRequest{Params: json.RawMessage(params)})
			if want := arguments == "" || arguments == "{}"; reached != want {
				t.Fatalf("reached=%v, want %v", reached, want)
			}
		})
	}
}

func TestStorageArgumentsPreserveNumbersAndRemoveSelector(t *testing.T) {
	m := NewMCPClient(MCPClientConfig{BindStorage: func(ctx context.Context, _ string) (context.Context, error) {
		return WithDestination(ctx, Destination{Storage: "local"}), nil
	}})
	for _, prefix := range []string{"", `"storage":"local",`} {
		t.Run(prefix, func(t *testing.T) {
			raw := json.RawMessage(`{` + prefix + `"id":"n","big":1757600000000000123,"nested":{"small":-9007199254740993}}`)
			_, p, err := m.prepareStorageCall(t.Context(), kgtools.CallToolParams{Name: "query", Arguments: raw})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(p.Arguments), `"storage"`) || !strings.Contains(string(p.Arguments), "1757600000000000123") || !strings.Contains(string(p.Arguments), "-9007199254740993") {
				t.Fatalf("changed numeric values or retained storage: %s", p.Arguments)
			}
			if prefix == "" && &p.Arguments[0] != &raw[0] {
				t.Fatal("ordinary arguments unnecessarily copied")
			}
		})
	}
}

func TestStorageReferenceCopyOnlyWhenNeeded(t *testing.T) {
	ctx := WithDestination(t.Context(), Destination{Storage: "local"})
	req := &knowledgev1.ExecuteRequest{Target: &knowledgev1.GraphSelector{Graph: "knowledge"}, Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{NodeBodies: []*knowledgev1.NodeBody{{Id: "ordinary", Description: strings.Repeat("body", 1024)}}, SetMetadata: map[string]string{"plain": "content"}}}}
	_, got, err := resolveRequestReferences(ctx, req)
	if err != nil || got != req {
		t.Fatalf("reference-free request copied: same=%v err=%v", got == req, err)
	}
	ref := `kgref:2:{"storage":"local","graph":"knowledge","id":"peer"}`
	req.GetMutation().SetMetadata["link"] = ref
	_, got, err = resolveRequestReferences(ctx, req)
	if err != nil || got.GetMutation().SetMetadata["link"] != ref {
		t.Fatalf("metadata qualification lost: %v %v", got, err)
	}
	req.GetMutation().NodeBodies[0].Id = ref
	_, got, err = resolveRequestReferences(ctx, req)
	if err != nil || got == req || got.GetMutation().NodeBodies[0].Id != "peer" || req.GetMutation().NodeBodies[0].Id != ref {
		t.Fatalf("endpoint rewrite changed original or failed: %v %v", got, err)
	}
	cloud := WithDestination(t.Context(), Destination{Storage: "cloud", AccountID: "a"})
	req.GetMutation().NodeBodies[0].Id = "ordinary"
	req.GetMutation().SetMetadata["link"] = `["\u006bgref:2:{\"storage\":\"local\",\"graph\":\"knowledge\",\"id\":\"peer\"}"]`
	if _, _, err = resolveRequestReferences(cloud, req); err == nil {
		t.Fatal("escaped serialized local reference bypassed cloud validation")
	}
}
