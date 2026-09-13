// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// Real SSH forwarding peer adapted from Agent web/wasm/httpbridge_ssh_test.go.
// The relay and VM are fixture services; production native transport does every dial.
func dashboardPeer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	signer := func() ssh.Signer {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		s, err := ssh.NewSignerFromKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	userCA, hostCA, hostKey := signer(), signer(), signer()

	hostSigners := map[string]ssh.Signer{"raw": hostKey}
	for _, mode := range []string{"valid", "wrong-principal", "expired", "wrong-ca"} {
		principal := "env-native"
		expiry := time.Now().Add(time.Hour)
		ca := hostCA
		if mode == "wrong-principal" {
			principal = "other-environment"
		}
		if mode == "expired" {
			expiry = time.Now().Add(-time.Second)
		}
		if mode == "wrong-ca" {
			ca = signer()
		}
		cert := &ssh.Certificate{Key: hostKey.PublicKey(), CertType: ssh.HostCert, ValidPrincipals: []string{principal}, ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(expiry.Unix())}
		if err := cert.SignCert(rand.Reader, ca); err != nil {
			t.Fatal(err)
		}
		signed, err := ssh.NewCertSigner(cert, hostKey)
		if err != nil {
			t.Fatal(err)
		}
		hostSigners[mode] = signed
	}
	var hostMode atomic.Value
	hostMode.Store("valid")
	checker := ssh.CertChecker{IsUserAuthority: func(key ssh.PublicKey) bool { return bytes.Equal(key.Marshal(), userCA.PublicKey().Marshal()) }}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var requests, writes, mints, handshakes, barrierRequests atomic.Int64
	barrier := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" && r.Method != "HEAD" {
			writes.Add(1)
		}
		switch r.URL.Path {
		case "/barrier":
			if barrierRequests.Add(1) == 3 {
				close(barrier)
			}
			select {
			case <-barrier:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "overlap")
		case "/interrupted":
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, "partial")
		case "/malformed":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, "HTTP/1.1 invalid\r\n\r\n")
		case "/error":
			http.Error(w, "upstream rejected", 502)
		case "/redirect":
			http.Redirect(w, r, "http://elsewhere.invalid/secret", 302)
		case "/oversize":
			_, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+1))
		case "/empty":
			w.WriteHeader(204)
		default:
			body, _ := io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{"canary": "dashboard-public-canary", "message": "Native SSH dashboard data", "method": r.Method, "body": string(body), "path": r.URL.RequestURI()})
		}
	}))
	t.Cleanup(upstream.Close)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				config := &ssh.ServerConfig{PublicKeyCallback: checker.Authenticate}
				config.AddHostKey(hostSigners[hostMode.Load().(string)])
				server, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				handshakes.Add(1)
				go ssh.DiscardRequests(reqs)
				for incoming := range chans {
					if incoming.ChannelType() != "direct-tcpip" {
						_ = incoming.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					go func(ch ssh.NewChannel) {
						var destination struct {
							Host       string
							Port       uint32
							Origin     string
							OriginPort uint32
						}
						if ssh.Unmarshal(ch.ExtraData(), &destination) != nil || destination.Host != dashboardContainerIP || destination.Port != 8080 {
							_ = ch.Reject(ssh.Prohibited, "wrong destination")
							return
						}
						channel, rs, err := ch.Accept()
						if err != nil {
							return
						}
						defer channel.Close()
						go ssh.DiscardRequests(rs)
						remote, err := net.Dial("tcp", strings.TrimPrefix(upstream.URL, "http://"))
						if err != nil {
							return
						}
						defer remote.Close()
						done := make(chan struct{}, 1)
						go func() { _, _ = io.Copy(remote, channel); done <- struct{}{} }()
						_, _ = io.Copy(channel, remote)
						_ = channel.Close()
						_ = remote.Close()
						<-done
					}(incoming)
				}
			}()
		}
	}()
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/fixture/host", func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("mode")
		if hostSigners[mode] == nil {
			http.Error(w, "unknown mode", 400)
			return
		}
		hostMode.Store(mode)
		w.WriteHeader(204)
	})
	mux.HandleFunc("/fixture/counts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"mints": mints.Load(), "handshakes": handshakes.Load(), "requests": requests.Load(), "writes": writes.Load()})
	})
	mux.HandleFunc("/v1/dev-vm/connect", func(w http.ResponseWriter, r *http.Request) {
		mints.Add(1)
		var input connectRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Env == "" {
			http.Error(w, "bad connect", 400)
			return
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(input.SSHPublicKey))
		if err != nil {
			http.Error(w, "bad key", 400)
			return
		}
		cert := &ssh.Certificate{Key: key, CertType: ssh.UserCert, ValidPrincipals: []string{sshLoginUser}, ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Minute).Unix()), Permissions: ssh.Permissions{Extensions: map[string]string{"permit-port-forwarding": ""}}}
		if err := cert.SignCert(rand.Reader, userCA); err != nil {
			t.Error(err)
			return
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"dev_env_id": "env-native"}).SignedString([]byte("fixture-relay"))
		if err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(connectResponse{Certificate: string(ssh.MarshalAuthorizedKey(cert)), RelayToken: token, HostCAPubKey: string(ssh.MarshalAuthorizedKey(hostCA.PublicKey()))})
	})
	mux.HandleFunc(wsProxyPath, func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var header proxyHeader
		if json.Unmarshal(raw, &header) != nil || header.DevEnvID != "env-native" {
			return
		}
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			return
		}
		defer conn.Close()
		done := make(chan struct{}, 1)
		go func() {
			defer func() { done <- struct{}{} }()
			buf := make([]byte, 32768)
			for {
				n, err := conn.Read(buf)
				if n > 0 {
					if ws.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		for {
			_, raw, err := ws.ReadMessage()
			if err != nil {
				break
			}
			if _, err = conn.Write(raw); err != nil {
				break
			}
		}
		_ = conn.Close()
		<-done
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &requests
}

func TestDesktopDashboardPeerProcess(t *testing.T) {
	ready := os.Getenv("DESKTOP_DASHBOARD_PEER_READY")
	if ready == "" {
		t.Skip("Electron dashboard peer fixture absent")
	}
	if !filepath.IsAbs(ready) || filepath.Base(ready) != "peer-url" {
		t.Fatal("invalid ready path")
	}
	peer, _ := dashboardPeer(t)
	root, err := os.OpenRoot(filepath.Dir(ready))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("peer-url", []byte(peer.URL), 0600); err != nil {
		t.Fatal(err)
	}
	<-t.Context().Done()
}
