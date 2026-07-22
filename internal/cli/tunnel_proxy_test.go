// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

// mintRelayToken mints a relay_token carrying a dev_env_id claim. ParseUnverified
// never checks the signature, so any signing method + secret suffices for the test.
func mintRelayToken(t *testing.T, devEnvID string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"dev_env_id": devEnvID,
		"account":    "acct-x",
	})
	s, err := tok.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("mint relay_token: %v", err)
	}
	return s
}

// TestProxyHeader_WireKeys freezes the ws first-message contract: proxyHeader must
// marshal to EXACTLY dev_env_id + relay_token — the tags the relay's wsHeader
// (agent repo) mirrors. A rename here silently breaks the cross-repo contract.
func TestProxyHeader_WireKeys(t *testing.T) {
	b, err := json.Marshal(proxyHeader{DevEnvID: "d", RelayToken: "r"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("proxyHeader must have exactly two wire fields; got %v", m)
	}
	for _, key := range []string{"dev_env_id", "relay_token"} {
		if _, ok := m[key]; !ok {
			t.Errorf("proxyHeader JSON missing %q; got %v", key, m)
		}
	}
}

// TestDevEnvIDFromToken asserts the dev_env_id claim is extracted from a relay_token
// via ParseUnverified (no signature check), and a token without the claim errors.
func TestDevEnvIDFromToken(t *testing.T) {
	tok := mintRelayToken(t, "env-abc")
	got, err := devEnvIDFromToken(tok)
	if err != nil {
		t.Fatalf("devEnvIDFromToken: %v", err)
	}
	if got != "env-abc" {
		t.Errorf("dev_env_id = %q, want env-abc", got)
	}

	noClaim := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"account": "a"})
	s, _ := noClaim.SignedString([]byte("secret"))
	if _, err := devEnvIDFromToken(s); err == nil {
		t.Error("a token without a dev_env_id claim must error")
	}
	if _, err := devEnvIDFromToken("not.a.jwt"); err == nil {
		t.Error("a malformed token must error")
	}
}

// TestProxyOverWS_HeaderAndPipe drives the transport core against an httptest ws
// server: the FIRST message is a TEXT proxyHeader whose dev_env_id equals the
// relay_token's decoded claim; then bytes written to stdin arrive as BINARY ws
// messages and binary ws messages arrive on stdout. This is exactly runProxy's
// dial+header+pipe leg (runProxy delegates to proxyOverWS with the derived header).
func TestProxyOverWS_HeaderAndPipe(t *testing.T) {
	const devEnvID = "env-xyz"
	relayToken := mintRelayToken(t, devEnvID)

	type serverObs struct {
		hdr       proxyHeader
		fromStdin []byte
	}
	obsCh := make(chan serverObs, 1)
	release := make(chan struct{})

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		// (1) first message must be the TEXT header.
		mt, msg, err := c.ReadMessage()
		if err != nil || mt != websocket.TextMessage {
			return
		}
		var h proxyHeader
		_ = json.Unmarshal(msg, &h)

		// (2) send a binary message the client must surface on stdout.
		_ = c.WriteMessage(websocket.BinaryMessage, []byte("from-relay"))

		// (3) read the binary message the client wrote from stdin.
		_, b, _ := c.ReadMessage()
		obsCh <- serverObs{hdr: h, fromStdin: append([]byte(nil), b...)}

		<-release // hold the conn open so the client drives teardown
	}))
	defer srv.Close()
	defer close(release)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + wsProxyPath

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	devEnvIDDerived, err := devEnvIDFromToken(relayToken)
	if err != nil {
		t.Fatalf("derive dev_env_id: %v", err)
	}
	hdr := proxyHeader{DevEnvID: devEnvIDDerived, RelayToken: relayToken}

	retCh := make(chan error, 1)
	go func() {
		retCh <- proxyOverWS(context.Background(), wsURL, hdr, stdinR, stdoutW)
	}()

	// stdout direction: the server's "from-relay" binary must arrive on stdout.
	out := make([]byte, len("from-relay"))
	if _, err := io.ReadFull(stdoutR, out); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(out) != "from-relay" {
		t.Errorf("stdout = %q, want from-relay", out)
	}

	// stdin direction: bytes written to stdin must arrive as a binary ws message.
	if _, err := stdinW.Write([]byte("to-relay")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}

	var obs serverObs
	select {
	case obs = <-obsCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server to observe the header + stdin bytes")
	}

	// The first TEXT message decoded to {dev_env_id, relay_token} with dev_env_id
	// equal to the relay_token's decoded claim.
	if obs.hdr.RelayToken != relayToken {
		t.Errorf("header relay_token = %q, want the fetched token", obs.hdr.RelayToken)
	}
	claim, err := devEnvIDFromToken(obs.hdr.RelayToken)
	if err != nil {
		t.Fatalf("decode header token claim: %v", err)
	}
	if obs.hdr.DevEnvID != claim || obs.hdr.DevEnvID != devEnvID {
		t.Errorf("header dev_env_id = %q, want %q (== the token's decoded claim)", obs.hdr.DevEnvID, devEnvID)
	}
	if string(obs.fromStdin) != "to-relay" {
		t.Errorf("stdin bytes reached the relay as %q, want to-relay", obs.fromStdin)
	}

	// Clean teardown: closing stdin ends the pump; proxyOverWS returns nil.
	_ = stdinW.Close()
	select {
	case err := <-retCh:
		if err != nil {
			t.Errorf("proxyOverWS returned %v, want nil on clean stdin EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxyOverWS did not return after stdin EOF")
	}
	_ = stdoutR.Close()
}
