// SPDX-License-Identifier: Apache-2.0

package bootstrap

// version_probe_leak_test.go pins the connection OWNERSHIP half of the daemon
// version probe, which the functional tests in subcommands_test.go cannot see.
//
// WHY A SEPARATE PROPERTY. TestProbeDaemonVersion already covers both arms of
// the best-effort contract — dead port yields ("", false), live stub yields the
// version — and both of those round trips COMPLETE, so the probe's cleartext
// HTTP/2 connection is idle by the time the probe returns and a pool-level
// release reaches it. The arm neither covers is the one where the budget
// expires WHILE the request is still in flight: the connection then still
// carries the aborted stream, a pool-level release skips it, and the transport
// holds no idle timeout that would reap it later. The abandoned connection's
// read loop — net/http.(*http2clientConnReadLoop).run, from the bundled HTTP/2
// implementation — then lives for the rest of the process. No test that calls
// the probe can see this, because the goroutine is attributed to the package's
// goleak gate at suite end rather than to the test that opened it.
//
// WHY THE ASSERTION NAMES ONE GOROUTINE rather than calling goleak.Find. The
// stalled stub's own handler and serve goroutines are alive at assertion time
// by construction, so a whole-process leak check would report them alongside
// the finding and could not distinguish the two. The bundled-HTTP/2 CLIENT read
// loop is unambiguous: newVersionProbeClient is the only thing in this binary
// that builds a net/http transport with unencrypted HTTP/2 enabled, so that
// frame on the stack can only be a probe connection nobody closed.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// probeReadLoopFrame is the stack frame of a bundled-HTTP/2 client connection's
// read loop. It is the goroutine the package's suite-level goleak gate reports
// when a version probe abandons its connection.
const probeReadLoopFrame = "net/http.(*http2clientConnReadLoop).run"

// TestVersionProbeBudget pins the shipped round-trip budget. The leak test
// below deliberately runs the probe on a millisecond budget, and without this
// assertion a change that shortened the real budget to match would look like it
// had broken nothing.
func TestVersionProbeBudget(t *testing.T) {
	assert.Equal(t, 2*time.Second, versionProbeBudget,
		"probeDaemonVersion's shipped budget mirrors checkServer's (doctor_checks.go); "+
			"the shortened budget in TestProbeDaemonVersion_ReleasesConnectionWhenBudgetExpiresMidRequest is a test seam, not this value")
}

// TestProbeDaemonVersion_ReleasesConnectionWhenBudgetExpiresMidRequest proves
// the probe closes the cleartext-HTTP/2 connection it dialed even when its
// deadline fires before the response arrives.
//
// The stub server is deliberately left OPEN across the assertion.
// srv.CloseClientConnections() would tear the connection down from the peer's
// side and reap the client's read loop as a side effect, which would make this
// test pass against a probe that owns nothing at all.
//
// The probe is driven repeatedly because the unfixed failure is a race, not a
// certainty: when the deadline fires, the aborted stream is forgotten by the
// transport's own goroutine, and a pool-level release that happens to run after
// that does close the connection. That race resolves the leaky way about nine
// times in ten, so one attempt is a ~90% gate and this many attempts is a
// ~1-in-10^20 one. A probe that owns its connection passes every attempt.
func TestProbeDaemonVersion_ReleasesConnectionWhenBudgetExpiresMidRequest(t *testing.T) {
	const (
		budget   = 150 * time.Millisecond
		stall    = 500 * time.Millisecond // > budget, so the deadline always fires mid-stream
		attempts = 20
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(stall)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"version":"v0.0.0-stalled"}}}`)
	})
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	port := portFromURL(t, srv.URL)

	require.False(t, goroutinesContain(probeReadLoopFrame),
		"a bundled-HTTP/2 client read loop is already running before this test probes anything — "+
			"an earlier test in this package leaked one, and the assertion below would blame this test for it")

	for i := range attempts {
		v, ok := probeDaemonVersionWithin(port, budget)
		require.Falsef(t, ok, "attempt %d: the stub stalls past the budget, so the probe must report the daemon unknown", i)
		require.Emptyf(t, v, "attempt %d: a probe that timed out must return no version", i)
	}

	// Retry the read: closing the connection ends the read loop asynchronously,
	// so a single immediate sample would be a race in the passing direction.
	deadline := time.Now().Add(5 * time.Second)
	for goroutinesContain(probeReadLoopFrame) {
		if time.Now().After(deadline) {
			t.Fatalf("after %d probes whose budget expired mid-request, a bundled-HTTP/2 client read loop is still running: "+
				"the probe abandoned the connection it dialed, and that goroutine (plus the peer's serve goroutines) "+
				"outlives the call that opened it.\n%s", attempts, goroutinesMatching(probeReadLoopFrame))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// goroutinesContain reports whether any goroutine in this process currently has
// frame on its stack.
func goroutinesContain(frame string) bool {
	return strings.Contains(allGoroutineStacks(), frame)
}

// goroutinesMatching returns the stacks of every goroutine carrying frame, for
// a failure message that shows the leak rather than merely asserting it.
func goroutinesMatching(frame string) string {
	var keep []string
	for stack := range strings.SplitSeq(allGoroutineStacks(), "\n\n") {
		if strings.Contains(stack, frame) {
			keep = append(keep, stack)
		}
	}
	return strings.Join(keep, "\n\n")
}

func allGoroutineStacks() string {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}
