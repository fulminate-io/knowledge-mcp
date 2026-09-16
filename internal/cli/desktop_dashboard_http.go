// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// Native adaptation of Agent web/wasm/httpbridge.go. The saved endpoint is the
// authority; the renderer supplies only its identity and authored input payload.
const dashboardContainerIP = "10.201.0.2"

var dashboardRelayURL = func() (string, error) { return proxyWSURL(CloudEndpoint) }

type dashboardHTTPResponse struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Truncated bool              `json:"truncated"`
}

func requestDashboardEndpoint(ctx context.Context, t *auth.Transport, r dashboardRequest, doc dashboardDocument) (dashboardHTTPResponse, string) {
	var zero dashboardHTTPResponse
	raw := doc.Published
	if r.Draft || len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = doc.Draft
	}
	var envelope dashboardEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return zero, "invalid_response"
	}
	endpoint, code := savedDashboardEndpoint(envelope, r.Endpoint)
	if code != "" {
		return zero, code
	}
	path, err := dashboardEndpointPath(*endpoint)
	if err != nil {
		return zero, "invalid_endpoint"
	}
	// A read initiated by viewing, polling or Refresh must remain a read even if
	// the saved definition changed after the renderer loaded it.
	if r.Operation == "read" && endpoint.Method != "" && endpoint.Method != http.MethodGet && endpoint.Method != http.MethodHead {
		return zero, "dashboard_changed"
	}
	raw, err = t.Environment(ctx, r.Account, "get", r.Environment)
	if err != nil {
		return zero, remoteErrorCode(err, "get")
	}
	var env struct {
		ID    string `json:"env_id"`
		Name  string `json:"name"`
		State string `json:"state"`
		Port  int    `json:"hosted_port"`
	}
	if json.Unmarshal(raw, &env) != nil || env.ID != r.Environment || env.Name == "" {
		return zero, "invalid_response"
	}
	if env.State != "running" {
		return zero, "environment_stopped"
	}
	if env.Port <= 0 || env.Port > 65535 {
		return zero, "no_hosted_port"
	}
	key, err := generateEphemeralKey()
	if err != nil {
		return zero, "unavailable"
	}
	body, err := json.Marshal(connectRequest{SSHPublicKey: key.authorizedKey, Env: env.Name})
	if err != nil {
		return zero, "unavailable"
	}
	raw, err = t.DashboardConnect(ctx, r.Account, body)
	if err != nil {
		return zero, remoteErrorCode(err, "get")
	}
	var capability connectResponse
	if json.Unmarshal(raw, &capability) != nil {
		return zero, "invalid_response"
	}
	relayEnv, err := devEnvIDFromToken(capability.RelayToken)
	if err != nil || relayEnv != r.Environment {
		return zero, "connection_refused"
	}
	client, closeConnection, err := dashboardSSH(ctx, key, capability, r.Environment)
	if err != nil {
		return zero, "connection_refused"
	}
	defer closeConnection()
	return dashboardHTTP(ctx, client, env.Port, *endpoint, path, r.Payload)
}

func dashboardHTTP(ctx context.Context, client *ssh.Client, port int, endpoint dashboardEndpoint, path string, payload json.RawMessage) (dashboardHTTPResponse, string) {
	var zero dashboardHTTPResponse
	addr := net.JoinHostPort(dashboardContainerIP, strconv.Itoa(port))
	// One request per fresh connection: no automatic retry of ambiguous writes.
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return client.DialContext(ctx, "tcp", addr) }, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	target, err := url.ParseRequestURI(path)
	if err != nil {
		return zero, "invalid_endpoint"
	}
	target.Scheme, target.Host = "http", addr
	request, err := http.NewRequestWithContext(ctx, endpoint.Method, target.String(), bytes.NewReader(payload))
	if err != nil {
		return zero, "invalid_endpoint"
	}
	if len(payload) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := hc.Do(request)
	if err != nil {
		return zero, "unavailable"
	}
	defer response.Body.Close()
	const limit = 1 << 20 // JSON string escaping stays inside the CLI's 8 MiB output bound.
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return zero, "incomplete_response"
	}
	if len(data) > limit {
		return zero, "response_too_large"
	}
	headers := map[string]string{}
	for _, name := range []string{"Content-Type", "Content-Length"} {
		if value := response.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return dashboardHTTPResponse{Status: response.StatusCode, Headers: headers, Body: string(data)}, ""
}
func savedDashboardEndpoint(envelope dashboardEnvelope, id string) (*dashboardEndpoint, string) {
	var endpoint *dashboardEndpoint
	for i := range envelope.Endpoints {
		if envelope.Endpoints[i].ID != id {
			continue
		}
		if endpoint != nil {
			return nil, "invalid_response"
		}
		endpoint = &envelope.Endpoints[i]
	}
	if endpoint == nil {
		return nil, "invalid_endpoint"
	}
	return endpoint, ""
}
func dashboardEndpointPath(endpoint dashboardEndpoint) (string, error) {
	switch endpoint.Method {
	// Empty/missing saved methods use net/http's existing GET default.
	case "", "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
	default:
		return "", errors.New("invalid method")
	}
	p := endpoint.Path
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "", errors.New("invalid path")
	}
	parsed, err := url.ParseRequestURI(p)
	if err != nil || parsed.Host != "" || parsed.Scheme != "" {
		return "", errors.New("invalid path")
	}
	if len(endpoint.Params) > 0 {
		q := parsed.Query()
		for k, v := range endpoint.Params {
			q.Add(k, fmt.Sprint(v))
		}
		parsed.RawQuery = q.Encode()
	}
	return parsed.String(), nil
}
func dashboardSSH(ctx context.Context, key *ephemeralKeyPair, capability connectResponse, env string) (*ssh.Client, func(), error) {
	fail := func() {}
	private, err := ssh.ParsePrivateKey(key.privatePEM)
	if err != nil {
		return nil, fail, err
	}
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(capability.Certificate))
	if err != nil {
		return nil, fail, err
	}
	cert, ok := public.(*ssh.Certificate)
	if !ok || cert.CertType != ssh.UserCert {
		return nil, fail, errors.New("invalid user certificate")
	}
	signer, err := ssh.NewCertSigner(cert, private)
	if err != nil {
		return nil, fail, err
	}
	ca, _, _, _, err := ssh.ParseAuthorizedKey([]byte(capability.HostCAPubKey))
	if err != nil {
		return nil, fail, err
	}
	checker := &ssh.CertChecker{IsHostAuthority: func(key ssh.PublicKey, _ string) bool { return bytes.Equal(key.Marshal(), ca.Marshal()) }}
	wsURL, err := dashboardRelayURL()
	if err != nil {
		return nil, fail, err
	}
	local, peer := net.Pipe()
	proxyCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer peer.Close()
		_ = proxyOverWS(proxyCtx, wsURL, proxyHeader{DevEnvID: env, RelayToken: capability.RelayToken}, peer, peer)
	}()
	stopContext := context.AfterFunc(ctx, func() { _ = local.Close(); _ = peer.Close() })
	closePipe := func() { stopContext(); cancel(); _ = local.Close(); _ = peer.Close(); <-done }
	if deadline, ok := ctx.Deadline(); ok {
		_ = local.SetDeadline(deadline)
	}
	conn, chans, reqs, err := ssh.NewClientConn(local, net.JoinHostPort(env, "22"), &ssh.ClientConfig{User: sshLoginUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: checker.CheckHostKey})
	if err != nil {
		closePipe()
		return nil, fail, err
	}
	client := ssh.NewClient(conn, chans, reqs)
	return client, func() { _ = client.Close(); closePipe() }, nil
}
