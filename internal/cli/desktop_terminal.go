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

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/ssh"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

type desktopTerminalRequest struct {
	Account     string `json:"account"`
	Environment string `json:"environment"`
	PublicKey   string `json:"ssh_public_key"`
}
type desktopTerminalResult struct {
	Account     string `json:"account,omitempty"`
	Environment string `json:"environment,omitempty"`
	Certificate string `json:"certificate,omitempty"`
	RelayToken  string `json:"relay_token,omitempty"`
	HostCA      string `json:"host_ca_pubkey,omitempty"`
	Code        string `json:"code,omitempty"`
}

func desktopTerminalCmd(args []string) error {
	// Bad configuration, refused before any work — see DesktopAuthCmd for why
	// this is a hard error rather than a JSON result code.
	if _, err := auth.CredentialNamespace(); err != nil {
		return err
	}
	if len(args) != 0 {
		return errors.New("invalid terminal arguments")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 16385))
	if err != nil || len(data) > 16384 {
		return errors.New("invalid terminal request")
	}
	var request desktopTerminalRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid terminal request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return json.NewEncoder(os.Stdout).Encode(performDesktopTerminal(ctx, request))
}
func performDesktopTerminal(ctx context.Context, request desktopTerminalRequest) desktopTerminalResult {
	fail := func(code string) desktopTerminalResult { return desktopTerminalResult{Code: code} }
	if !remoteID.MatchString(request.Account) || !remoteID.MatchString(request.Environment) || len(request.PublicKey) > 8192 {
		return fail("invalid_request")
	}
	public, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(request.PublicKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 || public.Type() != ssh.KeyAlgoED25519 {
		return fail("invalid_request")
	}
	store, err := openStore()
	if err != nil {
		return fail("sign_in_required")
	}
	refresh, err := store.Get(ctx, auth.KeyRefreshToken)
	if err != nil || refresh == "" {
		return fail("sign_in_required")
	}
	transport := nativeManagementTransport(store)
	raw, err := transport.Environment(ctx, request.Account, "get", request.Environment)
	if err != nil {
		return fail(remoteErrorCode(err, "get"))
	}
	var env struct {
		ID    string `json:"env_id"`
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &env) != nil || env.ID != request.Environment || env.Name == "" || len(env.Name) > 4096 {
		return fail("invalid_response")
	}
	if env.State != "running" {
		return fail("environment_stopped")
	}
	body, err := json.Marshal(connectRequest{SSHPublicKey: request.PublicKey, Env: env.Name})
	if err != nil {
		return fail("invalid_request")
	}
	raw, err = transport.DashboardConnect(ctx, request.Account, body)
	if err != nil {
		return fail(remoteErrorCode(err, "get"))
	}
	var capability connectResponse
	if json.Unmarshal(raw, &capability) != nil {
		return fail("invalid_response")
	}
	var claims struct {
		Account string `json:"account"`
		Env     string `json:"dev_env_id"`
		jwt.RegisteredClaims
	}
	_, _, claimErr := jwt.NewParser().ParseUnverified(capability.RelayToken, &claims)
	if claimErr != nil || claims.Account != request.Account {
		return fail("connection_refused")
	}
	envID, err := devEnvIDFromToken(capability.RelayToken)
	if err != nil || envID != request.Environment {
		return fail("connection_refused")
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(capability.Certificate))
	if err != nil {
		return fail("invalid_response")
	}
	cert, ok := key.(*ssh.Certificate)
	if !ok || cert.CertType != ssh.UserCert || !bytes.Equal(cert.Key.Marshal(), public.Marshal()) || cert.ValidBefore <= uint64(time.Now().Unix()) || cert.ValidBefore > uint64(time.Now().Add(5*time.Minute).Unix()) {
		return fail("invalid_response")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(capability.HostCAPubKey)); err != nil {
		return fail("invalid_response")
	}
	return desktopTerminalResult{Account: request.Account, Environment: request.Environment, Certificate: capability.Certificate, RelayToken: capability.RelayToken, HostCA: capability.HostCAPubKey}
}
