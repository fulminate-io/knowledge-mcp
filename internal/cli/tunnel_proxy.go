// SPDX-License-Identifier: Apache-2.0

// tunnel_proxy.go — the `knowledge tunnel --proxy <env>` SSH ProxyCommand
// transport. Invoked FROM an ssh ProxyCommand line, this process's stdin/stdout
// ARE the ssh client's byte transport: it dials the relay's ws connect ingress,
// sends the frozen {dev_env_id, relay_token} header, and pumps raw SSH bytes over
// the ws in both directions.
//
// OSS-CLEAN: this path imports NO agent/executor proto — the relay wraps each ws
// binary message into a TunnelFrame on ITS side. The knowledge client only ever
// sees opaque bytes + a JSON header, so the OSS module stays proto-free (pinned by
// tunnel_boundary_test.go).

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

// wsProxyPath is the relay's ws connect-ingress path. It MUST match the relay's
// bootstrap.WSPath and the Traefik ws IngressRoute PathPrefix (agent repo) — a
// duplicated wire constant across repos.
const wsProxyPath = "/v1/dev-vm/tunnel-ws"

// proxyHeader is the FROZEN first-message ws header the --proxy transport sends to
// the relay. Its JSON tags (dev_env_id, relay_token) MUST match the relay's
// wsHeader (cmd/relay/internal/relay/connect_ws.go) LITERAL-FOR-LITERAL. The two
// live in different repos and cannot share a type, so a json-tag test on each side
// guards the drift. dev_env_id is echoed for the relay's convenience only — the
// relay does the AUTHORITATIVE verified cross-check of the relay_token's own
// dev_env claim, so a wrong dev_env_id here cannot widen access.
type proxyHeader struct {
	DevEnvID   string `json:"dev_env_id"`
	RelayToken string `json:"relay_token"`
}

// runProxy is the ProxyCommand entrypoint: fetch the dev_env-scoped relay_token,
// derive the dev_env_id from its own claim, dial the relay ws, and pipe stdin/
// stdout. It is a SEPARATE connect call from the cert mint that spawned it — the
// ssh client owns the cert; this process owns only the relay transport.
func runProxy(ctx context.Context, apiURL, env string) error {
	token, err := tunnelToken(ctx)
	if err != nil {
		return err
	}

	// The --proxy path needs only the relay_token; the connect endpoint requires a
	// public key to mint a cert, so we generate a throwaway ephemeral key and
	// discard the returned certificate (the ssh client uses its own).
	kp, err := generateEphemeralKey()
	if err != nil {
		return err
	}
	// The --proxy transport verifies nothing itself (the ssh client it fronts owns host
	// verification via the known_hosts runTunnel wrote); it needs only the relay_token, so
	// the certificate and host_ca_pubkey are discarded here.
	_, relayToken, _, err := fetchCert(ctx, connectHTTPClient, apiURL, token, kp.authorizedKey, env)
	if err != nil {
		return err
	}
	if relayToken == "" {
		return fmt.Errorf("connect returned no relay_token — this deployment may predate ws proxy support")
	}

	devEnvID, err := devEnvIDFromToken(relayToken)
	if err != nil {
		return err
	}

	wsURL, err := proxyWSURL(apiURL)
	if err != nil {
		return err
	}

	return proxyOverWS(ctx, wsURL, proxyHeader{DevEnvID: devEnvID, RelayToken: relayToken}, stdinReader, stdoutWriter)
}

// devEnvIDFromToken reads the dev_env_id claim from the relay_token WITHOUT
// verifying its signature (jwt/v5 ParseUnverified). The value is used ONLY to echo
// dev_env_id in the ws header — never for a trust decision; the relay verifies the
// token's signature + dev_env claim authoritatively. The JWT payload is plain JSON,
// so this needs no proto import.
func devEnvIDFromToken(relayToken string) (string, error) {
	var claims jwt.MapClaims
	if _, _, err := jwt.NewParser().ParseUnverified(relayToken, &claims); err != nil {
		return "", fmt.Errorf("parse relay_token: %w", err)
	}
	id, _ := claims["dev_env_id"].(string)
	if id == "" {
		return "", fmt.Errorf("relay_token carries no dev_env_id claim")
	}
	return id, nil
}

// proxyWSURL derives the relay ws URL (wss://<host>/v1/dev-vm/tunnel-ws) from the
// API endpoint — the relay is served at the SAME HOST as the API, so the
// build-tag-pinned CloudEndpoint is the only prod/dev switch.
func proxyWSURL(apiURL string) (string, error) {
	host, err := tunnelSSHHost(apiURL)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "wss", Host: host, Path: wsProxyPath}
	return u.String(), nil
}

// stdinReader / stdoutWriter indirect os.Stdin/os.Stdout so tests can drive the
// pipe with in-memory streams. Production wires the real process stdio.
var (
	stdinReader  io.Reader = os.Stdin
	stdoutWriter io.Writer = os.Stdout
)

// proxyOverWS dials wsURL, sends the frozen header as the FIRST (TEXT) ws message,
// then pumps stdin→ws (binary) and ws→stdout with TWO goroutines (a serial copy
// would deadlock a bidi SSH session, mirroring the relay's two-pump splice). It
// returns nil on a clean end (either side closing) and the error otherwise.
func proxyOverWS(ctx context.Context, wsURL string, hdr proxyHeader, stdin io.Reader, stdout io.Writer) error {
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial relay ws %s: %w (status %d)", wsURL, err, resp.StatusCode)
		}
		return fmt.Errorf("dial relay ws %s: %w", wsURL, err)
	}
	defer func() { _ = conn.Close() }()

	hdrBytes, err := json.Marshal(hdr)
	if err != nil {
		return fmt.Errorf("encode ws header: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, hdrBytes); err != nil {
		return fmt.Errorf("send ws header: %w", err)
	}

	// Two pumps (a serial copy would deadlock a bidi SSH session, mirroring the
	// relay's two-pump splice); the first to error tears the connection down.
	errc := make(chan error, 2)
	go pumpStdinToWS(conn, stdin, errc)
	go pumpWSToStdout(conn, stdout, errc)

	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		if isBenignPipeEnd(err) {
			return nil
		}
		return err
	}
}

// pumpStdinToWS copies stdin to the ws as BINARY messages until either errors.
func pumpStdinToWS(conn *websocket.Conn, stdin io.Reader, errc chan<- error) {
	buf := make([]byte, 32*1024)
	for {
		n, rerr := stdin.Read(buf)
		if n > 0 {
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				errc <- werr
				return
			}
		}
		if rerr != nil {
			errc <- rerr
			return
		}
	}
}

// pumpWSToStdout copies BINARY ws messages to stdout until either errors. Stray
// control/text frames after the handshake are ignored.
func pumpWSToStdout(conn *websocket.Conn, stdout io.Writer, errc chan<- error) {
	for {
		mt, msg, rerr := conn.ReadMessage()
		if rerr != nil {
			errc <- rerr
			return
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		if _, werr := stdout.Write(msg); werr != nil {
			errc <- werr
			return
		}
	}
}

// isBenignPipeEnd reports whether err is a normal end of the piped session (either
// side closing) that should NOT surface as a ProxyCommand error.
func isBenignPipeEnd(err error) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	return websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
	)
}
