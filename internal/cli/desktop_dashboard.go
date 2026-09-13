// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

type dashboardRequest struct {
	Operation   string          `json:"operation"`
	Account     string          `json:"account"`
	Document    string          `json:"document,omitempty"`
	Environment string          `json:"environment,omitempty"`
	Endpoint    string          `json:"endpoint,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}
type dashboardDocument struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Environment string          `json:"env_id"`
	Draft       json.RawMessage `json:"draft_doc,omitempty"`
	Published   json.RawMessage `json:"published_doc,omitempty"`
}
type dashboardResult struct {
	Account   string                 `json:"account,omitempty"`
	Documents []dashboardDocument    `json:"documents"`
	Document  *dashboardDocument     `json:"document,omitempty"`
	Response  *dashboardHTTPResponse `json:"response,omitempty"`
	Code      string                 `json:"code,omitempty"`
}
type dashboardEndpoint struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Path   string         `json:"path"`
	Params map[string]any `json:"params"`
}
type dashboardEnvelope struct {
	Version   int                 `json:"version"`
	Puck      json.RawMessage     `json:"puck"`
	Endpoints []dashboardEndpoint `json:"endpoints"`
}

func desktopDashboardCmd(args []string) error {
	var raw []byte
	var err error
	if len(args) == 0 {
		raw, err = io.ReadAll(io.LimitReader(stdinReader, (128<<10)+1))
	} else if len(args) == 1 {
		raw = []byte(args[0])
	} else {
		return errors.New("invalid dashboard request")
	}
	if err != nil || len(raw) > 128<<10 {
		return errors.New("invalid dashboard request")
	}
	var request dashboardRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return errors.New("invalid dashboard request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("invalid dashboard request")
	}
	if !validDashboardRequest(request) {
		return errors.New("invalid dashboard request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return json.NewEncoder(os.Stdout).Encode(performDashboard(ctx, request))
}
func validDashboardRequest(r dashboardRequest) bool {
	if !remoteID.MatchString(r.Account) {
		return false
	}
	switch r.Operation {
	case "list":
		return r.Document == "" && r.Environment == "" && r.Endpoint == "" && len(r.Payload) == 0
	case "get":
		return remoteID.MatchString(r.Document) && r.Environment == "" && r.Endpoint == "" && len(r.Payload) == 0
	case "read":
		return remoteID.MatchString(r.Document) && remoteID.MatchString(r.Environment) && remoteID.MatchString(r.Endpoint) && len(r.Payload) == 0
	case "request":
		return remoteID.MatchString(r.Document) && remoteID.MatchString(r.Environment) && remoteID.MatchString(r.Endpoint) && len(r.Payload) <= 64<<10
	}
	return false
}
func validDashboardDocument(d dashboardDocument) bool {
	if !remoteID.MatchString(d.ID) || !remoteID.MatchString(d.Environment) || d.Name == "" || len(d.Name) > 4096 {
		return false
	}
	raw := d.Published
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = d.Draft
	}
	var env dashboardEnvelope
	if json.Unmarshal(raw, &env) != nil || env.Version != 1 || env.Endpoints == nil {
		return false
	}
	var puck struct {
		Content []json.RawMessage `json:"content"`
	}
	return json.Unmarshal(env.Puck, &puck) == nil && puck.Content != nil
}
func performDashboard(ctx context.Context, r dashboardRequest) dashboardResult {
	if !validDashboardRequest(r) {
		return dashboardResult{Code: "invalid_request"}
	}
	store, err := openStore()
	if err != nil {
		return dashboardResult{Code: "sign_in_required"}
	}
	refresh, err := store.Get(ctx, auth.KeyRefreshToken)
	if err != nil || refresh == "" {
		return dashboardResult{Code: "sign_in_required"}
	}
	transport := nativeManagementTransport(store)
	raw, err := transport.Dashboard(ctx, r.Account, r.Document)
	if err != nil {
		return dashboardResult{Code: remoteErrorCode(err, "get")}
	}
	out := dashboardResult{Account: r.Account}
	if r.Operation == "list" {
		var list struct {
			Documents []dashboardDocument `json:"documents"`
		}
		if json.Unmarshal(raw, &list) != nil || list.Documents == nil || len(list.Documents) > 10000 {
			return dashboardResult{Code: "invalid_response"}
		}
		for _, d := range list.Documents {
			if !validDashboardDocument(d) {
				return dashboardResult{Code: "invalid_response"}
			}
		}
		out.Documents = list.Documents
		return out
	}
	var doc dashboardDocument
	if json.Unmarshal(raw, &doc) != nil || doc.ID != r.Document || !validDashboardDocument(doc) {
		return dashboardResult{Code: "invalid_response"}
	}
	if r.Operation == "get" {
		out.Document = &doc
		return out
	}
	if doc.Environment != r.Environment {
		return dashboardResult{Code: "dashboard_changed"}
	}
	response, code := requestDashboardEndpoint(ctx, transport, r, doc)
	if code != "" {
		return dashboardResult{Code: code}
	}
	out.Response = &response
	return out
}
