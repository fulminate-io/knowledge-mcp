// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusalBody is the four-key payload the gateway sends with a version refusal.
// It is written out here rather than imported: the body's shape belongs to the
// gateway's repo, and a test that shared the client's own struct could not notice
// the client quietly changing which keys it reads.
func refusalBody(t *testing.T, minimum, clientVersion, upgrade, reason string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"minimum":         minimum,
		"client_version":  clientVersion,
		"platform":        "linux-amd64",
		"upgrade_command": upgrade,
		"reason":          reason,
	})
	require.NoError(t, err)
	return b
}

// wsRefusalServer serves a NON-101 response to the websocket handshake, which is
// what the gateway does when it refuses a client. It also tracks connection state
// so a leaked response body is observable rather than merely assumed.
type wsRefusalServer struct {
	srv *httptest.Server

	mu     sync.Mutex
	active map[net.Conn]struct{}
}

func newWSRefusalServer(t *testing.T, status int, contentType string, body []byte) *wsRefusalServer {
	t.Helper()
	s := &wsRefusalServer{active: map[net.Conn]struct{}{}}
	// Unstarted on purpose: ConnState must be installed before the server
	// goroutine reads it, or net/http reads the field while this function is
	// still writing it (a data race the race detector reports).
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	// ConnState is the instrument for the body-close row: a response body left
	// unread and unclosed keeps its connection in StateActive, while a closed one
	// reaches StateIdle or StateClosed. Registering it BEFORE any request is what
	// makes the observation real rather than retrospective.
	s.srv.Config.ConnState = func(c net.Conn, state http.ConnState) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch state {
		case http.StateActive:
			s.active[c] = struct{}{}
		case http.StateIdle, http.StateClosed, http.StateHijacked:
			delete(s.active, c)
		case http.StateNew:
		}
	}
	s.srv.Start()
	t.Cleanup(s.srv.Close)
	return s
}

// wsURL is the ws:// form of the test server's address. proxyOverWS dials whatever
// URL it is handed, so a plain httptest server serves the handshake fine.
func (s *wsRefusalServer) wsURL() string {
	return "ws://" + strings.TrimPrefix(s.srv.URL, "http://")
}

func (s *wsRefusalServer) activeConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// TestTunnelProxy_SurfacesInstructiveRefusal covers the ws handshake half of the
// tunnel's refusal surfacing: a user whose tunnel is refused must be told the
// minimum, their own version and the remedy, not handed a bare status number.
//
// THE TRUNCATION AND FALLBACK ROWS ARE THE DISCRIMINATING ONES. gorilla hands back
// at most 1024 body bytes on a non-101 response — that bound is ITS choice, not
// ours — so an implementation that assumes a complete JSON body passes the happy
// row and fails those two. The general-failure row is what proves the instructive
// path did not swallow ordinary handshake errors.
func TestTunnelProxy_SurfacesInstructiveRefusal(t *testing.T) {
	const (
		minimum = "v1.4.0"
		upgrade = "knowledge install"
	)

	t.Run("a refusal names the minimum, this client's version and the remedy", func(t *testing.T) {
		body := refusalBody(t, minimum, "v0.9.0", upgrade, "version_unverified")
		srv := newWSRefusalServer(t, http.StatusUpgradeRequired, "application/json", body)

		err := proxyOverWS(context.Background(), srv.wsURL(), proxyHeader{}, strings.NewReader(""), &strings.Builder{})

		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, minimum, "the refusal must name the MINIMUM the user needs to reach")
		assert.Contains(t, msg, upgrade, "the refusal must name the REMEDY")
		// The client's own version comes from the binary, not from the body, so it
		// is asserted as "some version is named" rather than against the fixture.
		assert.Contains(t, strings.ToLower(msg), "client",
			"the refusal must name the version THIS client reports")
		assert.NotRegexp(t, `^dial relay ws .*status \d+\)$`, msg,
			"a bare status line is exactly what this row exists to prevent")
	})

	t.Run("a refusal body larger than gorilla's slurp still yields an instructive error or a clean fallback", func(t *testing.T) {
		// The bound is gorilla's: it reads at most 1024 bytes of a non-101 body, so
		// the client sees a TRUNCATED prefix and the JSON will not parse. The
		// requirement is that it still reports a refusal the user can act on — and,
		// above all, that it does not turn a partial parse into a different error
		// class that hides the refusal entirely.
		padded := refusalBody(t, minimum, "v0.9.0", upgrade, "version_unverified")
		padded = append(padded[:len(padded)-1],
			[]byte(`,"padding":"`+strings.Repeat("x", 4096)+`"}`)...)
		require.Greater(t, len(padded), 1024, "the fixture must exceed gorilla's slurp to test the truncation")
		srv := newWSRefusalServer(t, http.StatusUpgradeRequired, "application/json", padded)

		err := proxyOverWS(context.Background(), srv.wsURL(), proxyHeader{}, strings.NewReader(""), &strings.Builder{})

		require.Error(t, err)
		msg := strings.ToLower(err.Error())
		assert.Contains(t, msg, "version",
			"a truncated refusal body must still be reported as a VERSION refusal, not as a parse failure")
		assert.NotContains(t, msg, "unexpected end of json",
			"a partial body must not surface as a JSON error — that hides the refusal")
	})

	t.Run("a non-refusal handshake failure still reports the plain status form", func(t *testing.T) {
		// The instructive path must not swallow the general case: a 500 with an
		// HTML body is an ordinary handshake failure and reads as one.
		srv := newWSRefusalServer(t, http.StatusInternalServerError, "text/html",
			[]byte("<html><body>upstream exploded</body></html>"))

		err := proxyOverWS(context.Background(), srv.wsURL(), proxyHeader{}, strings.NewReader(""), &strings.Builder{})

		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, fmt.Sprintf("status %d", http.StatusInternalServerError),
			"an ordinary handshake failure keeps the plain status form")
		assert.NotContains(t, strings.ToLower(msg), "upgrade to",
			"a 500 is not a version refusal and must not be dressed as one")
	})

	t.Run("no connection is left active after a failed handshake", func(t *testing.T) {
		// HONEST SCOPE, stated because the obvious reading of this row would be
		// wrong. This does NOT verify that the caller closes resp.Body, and it
		// CANNOT: on the bad-handshake path gorilla replaces the body with an
		// in-memory NopCloser over the buffered bytes (client.go:391) and closes the
		// underlying network connection itself (client.go:332). The caller's
		// `defer resp.Body.Close()` is therefore a no-op by construction on this
		// path — proven by execution rather than reasoned: deleting that defer
		// leaves this test green.
		//
		// What it DOES verify is connection hygiene end to end — that a refused
		// handshake leaves nothing active server-side — which is the property an
		// operator would actually notice. It is a CHARACTERIZATION GUARD on
		// gorilla's teardown, not a gate on our own close, and it is labeled as one
		// so nobody later reads it as covering a property it does not cover.
		for _, tc := range []struct {
			name   string
			status int
			body   []byte
		}{
			{"refusal", http.StatusUpgradeRequired, refusalBody(t, minimum, "v0.9.0", upgrade, "version_unverified")},
			{"general failure", http.StatusInternalServerError, []byte("boom")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				srv := newWSRefusalServer(t, tc.status, "application/json", tc.body)

				// The instrument moves: nothing is active before the request, and
				// the assertion below would read non-zero if a connection were held.
				require.Equal(t, 0, srv.activeConns())

				err := proxyOverWS(context.Background(), srv.wsURL(), proxyHeader{}, strings.NewReader(""), &strings.Builder{})
				require.Error(t, err)

				// The server observes the close on its own goroutine after the client
				// returns, so the count is read with a bounded wait, not once.
				assert.Eventually(t, func() bool { return srv.activeConns() == 0 }, 2*time.Second, 10*time.Millisecond,
					"a failed handshake left a connection active server-side")
			})
		}
	})
}
