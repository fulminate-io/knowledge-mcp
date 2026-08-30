// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"strings"
	"testing"
)

// fixture: `lsof -nP -iTCP:54321` output where the client process (PID 4242)
// owns the connection whose LOCAL side is the ephemeral port 54321, and the
// daemon (PID 9999) holds the accepted socket where 54321 is the REMOTE side.
const lsofITCPFixture = `COMMAND     PID     USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
node       4242 jonathan   23u  IPv4 0xaa11bb22cc33dd44      0t0  TCP 127.0.0.1:54321->127.0.0.1:15023 (ESTABLISHED)
knowledge  9999 jonathan   12u  IPv4 0x44dd33cc22bb11aa      0t0  TCP 127.0.0.1:15023->127.0.0.1:54321 (ESTABLISHED)`

func TestParsePeerPID_LocalSideAndSelfExclusion(t *testing.T) {
	// selfPID = 9999 (the daemon): even though its accepted-socket line
	// mentions :54321 (on the remote side), it must be excluded both by the
	// self-PID guard and by the local-side requirement.
	pid, comm, err := parsePeerPID(lsofITCPFixture, 15023, 54321, 9999)
	if err != nil {
		t.Fatalf("parsePeerPID: unexpected error: %v", err)
	}
	if pid != 4242 {
		t.Fatalf("parsePeerPID: got PID %d, want 4242 (the client whose LOCAL side is :54321)", pid)
	}
	// The COMMAND column (fields[0]) is retained — comm names the peer
	// harness process in the resolution log line.
	if comm != "node" {
		t.Fatalf("parsePeerPID: got comm %q, want \"node\" (the COMMAND column of the client line)", comm)
	}
}

func TestParsePeerPID_SelfExclusionWhenSelfIsLocalSide(t *testing.T) {
	// If the only matching local-side line is the daemon's own PID, it must
	// be excluded and the resolve must fail rather than return self.
	out := `COMMAND     PID     USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
knowledge  9999 jonathan   12u  IPv4 0x44dd33cc22bb11aa      0t0  TCP 127.0.0.1:54321->127.0.0.1:15023 (ESTABLISHED)`
	if _, _, err := parsePeerPID(out, 15023, 54321, 9999); err == nil {
		t.Fatal("parsePeerPID: expected error when the only local-side line is the daemon's own PID")
	}
}

func TestParsePeerPID_NoMatch(t *testing.T) {
	if _, _, err := parsePeerPID(lsofITCPFixture, 15023, 11111, 9999); err == nil {
		t.Fatal("parsePeerPID: expected error when no connection has the local port")
	}
}

func TestParseCwdFn(t *testing.T) {
	fixture := "p4242\nfcwd\nn/Users/jonathan/code/agent\n"
	cwd, err := parseCwdFn(fixture)
	if err != nil {
		t.Fatalf("parseCwdFn: unexpected error: %v", err)
	}
	if cwd != "/Users/jonathan/code/agent" {
		t.Fatalf("parseCwdFn: got %q, want /Users/jonathan/code/agent", cwd)
	}
}

func TestParseCwdFn_NoField(t *testing.T) {
	if _, err := parseCwdFn("p4242\nfcwd\n"); err == nil {
		t.Fatal("parseCwdFn: expected error when no n-prefixed field present")
	}
}

// TestResolvePeerCwd_EndToEndViaSeam exercises the full resolvePeerCwd flow
// through the injected command-runner seam: the first lsof call (-iTCP:port)
// returns the connection fixture, the second (-p pid -d cwd -Fn) returns the
// cwd fixture. No live socket is touched.
func TestResolvePeerCwd_EndToEndViaSeam(t *testing.T) {
	orig := peerCwdRunner
	t.Cleanup(func() { peerCwdRunner = orig })

	peerCwdRunner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "-iTCP:54321"):
			return []byte(lsofITCPFixture), nil
		case strings.Contains(joined, "-p 4242"):
			return []byte("p4242\nfcwd\nn/Users/jonathan/code/knowledge\n"), nil
		default:
			t.Fatalf("unexpected lsof invocation: %v", args)
			return nil, nil
		}
	}

	cwd, pid, comm, err := resolvePeerCwd(context.Background(), 15023, 54321)
	if err != nil {
		t.Fatalf("resolvePeerCwd: unexpected error: %v", err)
	}
	if cwd != "/Users/jonathan/code/knowledge" {
		t.Fatalf("resolvePeerCwd: got %q, want /Users/jonathan/code/knowledge", cwd)
	}
	if pid != 4242 || comm != "node" {
		t.Fatalf("resolvePeerCwd: got pid=%d comm=%q, want pid=4242 comm=\"node\"", pid, comm)
	}
}
