// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
)

// fakeClock is the injected clock. It never sleeps: the loop's waits are driven
// by advancing this clock between polls, so the whole schedule is exercised in
// milliseconds of wall time.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// proofSeamFake counts attempts and answers with a scripted expiry or error.
type proofSeamFake struct {
	mu       sync.Mutex
	attempts int
	respond  func(n int) (time.Time, error)
}

func (f *proofSeamFake) call(context.Context, func() (*auth.Transport, error)) (time.Time, error) {
	f.mu.Lock()
	f.attempts++
	n := f.attempts
	respond := f.respond
	f.mu.Unlock()
	return respond(n)
}

func (f *proofSeamFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// installProofSeam swaps the proof seam and the clock for the test's duration.
func installProofSeam(t *testing.T, f *proofSeamFake, clk *fakeClock) {
	t.Helper()
	prevSeam, prevNow := versionProofOnce, nowFn
	versionProofOnce = f.call
	nowFn = clk.Now
	t.Cleanup(func() {
		versionProofOnce = prevSeam
		nowFn = prevNow
		clientver.ClearRefusal()
		clientver.ClearProof()
	})
	clientver.ClearRefusal()
	clientver.ClearProof()
}

// noTransport is the transport factory the fake never dereferences.
func noTransport() (*auth.Transport, error) { return nil, nil }

// alwaysIn is the always-authenticated cloud signal. The logged-OUT case is
// driven by a per-test closure rather than a shared helper, because that case
// also needs to FLIP mid-test.
func alwaysIn(context.Context) bool { return true }

// waitFor polls cond until it holds or the budget expires. The budget bounds a
// scheduling race, never a simulated interval — every simulated interval is
// advanced on the fake clock.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

const testPoll = 2 * time.Millisecond

func TestVersionProofLoop_ProvesAtStartBeforeTTLAndOnRefusal(t *testing.T) {
	t.Run("proves at start with NO boot delay", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		fake := &proofSeamFake{respond: func(int) (time.Time, error) {
			return clk.Now().Add(time.Hour), nil
		}}
		installProofSeam(t, fake, clk)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runVersionProofLoopEvery(ctx, testPoll, noTransport, alwaysIn) }()

		// The FIRST attempt happens before any clock advance at all. The
		// transcript template this loop is modeled on delays a minute; copying
		// that delay would leave every restart with a window of refused calls,
		// and this leg is what catches it.
		waitFor(t, "the first proof with no clock advance", func() bool { return fake.count() >= 1 })
		cancel()
		<-done

		p, ok := clientver.LastProof()
		if !ok || !p.OK {
			t.Fatalf("every attempt must record a ProofState; got ok=%v state=%+v", ok, p)
		}
	})

	t.Run("schedules from the returned expiry, not a fixed constant", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		// Two DIFFERENT expiries: the schedule must move with them.
		fake := &proofSeamFake{respond: func(n int) (time.Time, error) {
			if n == 1 {
				return clk.Now().Add(90 * time.Minute), nil
			}
			return clk.Now().Add(4 * time.Hour), nil
		}}
		installProofSeam(t, fake, clk)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runVersionProofLoopEvery(ctx, testPoll, noTransport, alwaysIn) }()
		waitFor(t, "the first proof", func() bool { return fake.count() >= 1 })

		// 90m expiry minus the 15m margin = 75m. Just short of it, nothing
		// fires: this is the leg that separates "scheduled from expires_at"
		// from "scheduled on a hardcoded hour".
		clk.Advance(70 * time.Minute)
		time.Sleep(20 * time.Millisecond)
		if got := fake.count(); got != 1 {
			t.Fatalf("a second proof fired %d minutes early; the schedule is not derived from the returned expiry (attempts=%d)", 5, got)
		}
		clk.Advance(10 * time.Minute)
		waitFor(t, "the second proof once the first expiry's window arrives", func() bool { return fake.count() >= 2 })

		// The SECOND response carried a 4h expiry, so the third proof must sit
		// far beyond where the first schedule would have put it.
		clk.Advance(80 * time.Minute)
		time.Sleep(20 * time.Millisecond)
		if got := fake.count(); got != 2 {
			t.Errorf("a third proof fired on the FIRST response's cadence (attempts=%d); the schedule is not tracking each response's own expiry", got)
		}
		cancel()
		<-done
	})

	t.Run("floors the delay on a near or past expiry", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		// A WELL-FORMED expiry at or before now — what a reduced gateway TTL, a
		// large clock skew, or a gateway bug produces. It parses, and the proof
		// SUCCEEDED, so neither the refusal spacing nor the failure cadence
		// applies: only the success-path floor stands between this and an
		// unbounded outbound loop.
		fake := &proofSeamFake{respond: func(int) (time.Time, error) {
			return clk.Now().Add(-time.Second), nil
		}}
		installProofSeam(t, fake, clk)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runVersionProofLoopEvery(ctx, testPoll, noTransport, alwaysIn) }()
		waitFor(t, "the first proof", func() bool { return fake.count() >= 1 })

		// Advance ONE HOUR of simulated time. Unfloored, the delay is negative
		// and the loop re-proves on every poll — hundreds of attempts. Floored
		// at 5 minutes, an hour permits at most 12 further attempts.
		const simulated = time.Hour
		const permitted = int(simulated/minReproveInterval) + 1
		for range 60 {
			clk.Advance(time.Minute)
			time.Sleep(2 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		got := fake.count()
		if got > permitted+1 {
			t.Errorf("%d proofs in one simulated hour against a past expiry; the success-path delay is not floored at %s and a hostile or broken expiry becomes an outbound storm",
				got, minReproveInterval)
		}
		// KNOWN-POSITIVE for the same measurement: the loop DID keep proving,
		// so the bound above is a bound rather than a stalled loop.
		if got < 2 {
			t.Errorf("only %d proof(s) in a simulated hour; the loop stalled rather than being floored, so the bound above proves nothing", got)
		}
		cancel()
		<-done
	})

	t.Run("a missing expiry is treated as a failed proof, not assumed to be an hour", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		fake := &proofSeamFake{respond: func(int) (time.Time, error) {
			return time.Time{}, nil // verified, but no usable expiry
		}}
		installProofSeam(t, fake, clk)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runVersionProofLoopEvery(ctx, testPoll, noTransport, alwaysIn) }()
		waitFor(t, "the first proof", func() bool { return fake.count() >= 1 })

		// The FAILURE cadence, not the healthy one: well short of an hour.
		clk.Advance(proofFailureInterval + time.Second)
		waitFor(t, "a retry on the failure cadence", func() bool { return fake.count() >= 2 })
		cancel()
		<-done
	})

	t.Run("a failed proof records state, leaves the latch set, and does not stop the loop", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		fake := &proofSeamFake{respond: func(int) (time.Time, error) {
			return time.Time{}, errors.New("gateway unreachable")
		}}
		installProofSeam(t, fake, clk)
		clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonUnverified, Minimum: "2.0.0"})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runVersionProofLoopEvery(ctx, testPoll, noTransport, alwaysIn) }()
		waitFor(t, "the first proof", func() bool { return fake.count() >= 1 })

		p, ok := clientver.LastProof()
		if !ok || p.OK || p.Err == "" {
			t.Errorf("a failed proof must record a failed ProofState naming the error; got ok=%v %+v", ok, p)
		}
		if _, refused := clientver.CurrentRefusal(); !refused {
			t.Errorf("a FAILED proof must leave the latch set, so the render surfaces keep showing a real refusal")
		}
		// The loop continues: a failed proof neither panics nor stops it.
		clk.Advance(proofFailureInterval + time.Second)
		waitFor(t, "the loop to continue past a failure", func() bool { return fake.count() >= 2 })
		cancel()
		<-done
	})

	t.Run("a successful proof clears the latch", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		fake := &proofSeamFake{respond: func(int) (time.Time, error) {
			return clk.Now().Add(time.Hour), nil
		}}
		installProofSeam(t, fake, clk)
		clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonUnverified})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runVersionProofLoopEvery(ctx, testPoll, noTransport, alwaysIn) }()
		waitFor(t, "the latch to clear after a successful proof", func() bool {
			_, refused := clientver.CurrentRefusal()
			return !refused
		})
		cancel()
		<-done
	})

	t.Run("a latched refusal triggers an immediate re-proof, bounded by the minimum spacing", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		// EVERY proof SUCCEEDS with a long expiry. That isolates the refusal
		// trigger as the only thing that can produce a further attempt: the
		// schedule is eight hours out and the failure cadence never applies.
		// The standing refusal is modeled the way it really arises — the loop's
		// success clears the latch, and the very next refused cloud request
		// re-latches it — by re-latching after each observation.
		fake := &proofSeamFake{respond: func(int) (time.Time, error) {
			return clk.Now().Add(8 * time.Hour), nil
		}}
		installProofSeam(t, fake, clk)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runVersionProofLoopEvery(ctx, testPoll, noTransport, alwaysIn) }()
		waitFor(t, "the first proof", func() bool { return fake.count() >= 1 })

		// A refusal lands. It must re-prove NOW, not in eight hours.
		clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonUnverified})
		waitFor(t, "an immediate refusal-driven re-proof", func() bool { return fake.count() >= 2 })

		// A client that can never be cleared stays refused and says so rather
		// than hammering the endpoint. Re-latch on every observation, exactly
		// as a stream of refused cloud requests would.
		after := fake.count()
		for range 20 {
			clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonUnverified})
			clk.Advance(10 * time.Second)
			time.Sleep(2 * time.Millisecond)
		}
		// 200 simulated seconds is inside the 5-minute spacing.
		if got := fake.count(); got != after {
			t.Errorf("a standing refusal produced %d extra attempts inside the %s minimum spacing; the trigger busy-loops",
				got-after, minRefusalReproveSpacing)
		}
		// KNOWN-POSITIVE: past the spacing, it DOES re-prove — so the silence
		// above is the bound working, not the trigger being dead.
		clientver.LatchRefusal(clientver.Refusal{Reason: clientver.ReasonUnverified})
		clk.Advance(minRefusalReproveSpacing + time.Second)
		waitFor(t, "a re-proof once the minimum spacing has passed", func() bool { return fake.count() > after })
		cancel()
		<-done
	})

	t.Run("no proof while logged out, and proving starts when the signal flips", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		fake := &proofSeamFake{respond: func(int) (time.Time, error) {
			return clk.Now().Add(time.Hour), nil
		}}
		installProofSeam(t, fake, clk)

		var mu sync.Mutex
		in := false
		loggedIn := func(context.Context) bool {
			mu.Lock()
			defer mu.Unlock()
			return in
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); runVersionProofLoopEvery(ctx, testPoll, noTransport, loggedIn) }()

		clk.Advance(30 * time.Minute)
		time.Sleep(20 * time.Millisecond)
		if got := fake.count(); got != 0 {
			t.Errorf("%d proof(s) attempted while logged out; the auth gate must short-circuit before a transport is built", got)
		}
		// A mid-session login begins proving with no restart.
		mu.Lock()
		in = true
		mu.Unlock()
		clk.Advance(proofFailureInterval + time.Second)
		waitFor(t, "proving to begin after the logged-in signal flips", func() bool { return fake.count() >= 1 })
		cancel()
		<-done
	})

	// THE WIRING LEG. Without it the eager OpenSelf call has no catcher
	// anywhere: the self-read criterion opens a handle itself through the stub
	// seam, every other leg here stubs the whole proof, and the exchange
	// criterion supplies its own answer function because the transport package
	// never imports the self-read one by design. An unwired OpenSelf would ship
	// all-green while every challenge answered with an unopened-handle error.
	t.Run("the real start path opens the executable handle BEFORE the loop proves", func(t *testing.T) {
		clk := &fakeClock{now: time.Now()}
		handleOpenAtFirstAttempt := make(chan bool, 1)
		fake := &proofSeamFake{respond: func(n int) (time.Time, error) {
			if n == 1 {
				// Ask the REAL self-read whether a handle is held. It answers
				// with an explicit error when none is.
				_, err := clientver.AnswerChallenge([]byte{0x01}, 0, 1)
				select {
				case handleOpenAtFirstAttempt <- err == nil || !isUnopenedHandle(err):
				default:
				}
			}
			return clk.Now().Add(time.Hour), nil
		}}
		installProofSeam(t, fake, clk)

		// The package's own zero-IO client: a Router over an empty auth store,
		// machine-auth true so the loop's cloud gate lets the attempt through.
		// It dials nothing — the proof seam is faked and the Router's cloud
		// client is built lazily by a path nothing here calls.
		c := newCloudStatusClient(true)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		c.maybeStartVersionProof(ctx)

		select {
		case open := <-handleOpenAtFirstAttempt:
			if !open {
				t.Fatalf("the first proof ran with NO executable handle open; the start path never called the self-read opener, so every real challenge would answer with an unopened-handle error and every cloud request would come back refused")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("the start path never attempted a proof")
		}
		cancel()
	})

	// The hop above maybeStartVersionProof that no unit test can construct: the
	// starter must actually be invoked from the daemon's background wiring
	// function. A starter nobody calls is the same shipped defect as an
	// OpenSelf nobody calls.
	t.Run("the daemon's background wiring invokes the proof starter", func(t *testing.T) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(".", "daemon.go"), nil, 0)
		if err != nil {
			t.Fatalf("parse daemon.go: %v", err)
		}
		var wiringFound, starterCalled bool
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "wireRuntimesBackground" || fn.Recv == nil {
				return true
			}
			wiringFound = true
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "maybeStartVersionProof" {
					starterCalled = true
				}
				return true
			})
			return true
		})
		// KNOWN-POSITIVE CONTROL: a census that could not find the function
		// would otherwise report the same silence as a missing call.
		if !wiringFound {
			t.Fatalf("this census never found wireRuntimesBackground in daemon.go, so it examined nothing; the selector is broken or the function moved")
		}
		if !starterCalled {
			t.Errorf("wireRuntimesBackground does not call maybeStartVersionProof, so the daemon opens no handle and runs no proof — every cloud request would come back refused with nothing local to explain it")
		}
	})
}

// isUnopenedHandle reports whether err is the self-read's no-handle refusal. It
// keys on the message because the self-read package returns a plain error there
// and this test is asserting the WIRING, not the error's type.
func isUnopenedHandle(err error) bool {
	return err != nil && strings.Contains(err.Error(), "before the executable handle was opened")
}
