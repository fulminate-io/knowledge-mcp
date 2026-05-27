// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// withStubExecutable replaces getExecutable for the duration of the
// test. The path doesn't need to exist for findServerBinary's
// same-dir branch — only filepath.Dir(path) matters.
func withStubExecutable(t *testing.T, path string) {
	t.Helper()
	prev := getExecutable
	getExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { getExecutable = prev })
}

// withStubExecutableErr replaces getExecutable with one that always
// returns an error. Models the "kernel can't tell us our own path"
// edge case.
func withStubExecutableErr(t *testing.T, e error) {
	t.Helper()
	prev := getExecutable
	getExecutable = func() (string, error) { return "", e }
	t.Cleanup(func() { getExecutable = prev })
}

// withPATH temporarily replaces $PATH so exec.LookPath in
// findServerBinary's fallback branch is forced to look only inside
// dirs we control.
func withPATH(t *testing.T, value string) {
	t.Helper()
	prev := os.Getenv("PATH")
	t.Setenv("PATH", value)
	t.Cleanup(func() { _ = os.Setenv("PATH", prev) })
}

func TestFindServerBinary_SiblingFound(t *testing.T) {
	tmp := t.TempDir()
	stubExe := filepath.Join(tmp, "stdio_stub")
	siblingName := serverBinaryName
	if runtime.GOOS == "windows" {
		siblingName += ".exe"
		stubExe += ".exe"
	}
	siblingPath := filepath.Join(tmp, siblingName)

	if err := os.WriteFile(siblingPath, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write sibling: %v", err)
	}
	withStubExecutable(t, stubExe)
	withPATH(t, "")

	got, err := findServerBinary()
	if err != nil {
		t.Fatalf("findServerBinary: %v", err)
	}
	gotR, _ := filepath.EvalSymlinks(got)
	wantR, _ := filepath.EvalSymlinks(siblingPath)
	if gotR != wantR {
		t.Fatalf("findServerBinary returned %q, want %q (resolved: %q vs %q)", got, siblingPath, gotR, wantR)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("findServerBinary returned non-absolute path %q", got)
	}
}

func TestFindServerBinary_PATHFallback(t *testing.T) {
	binDir := t.TempDir() // dir of "stdio binary" — NO sibling here
	pathDir := t.TempDir()

	stubExe := filepath.Join(binDir, "stdio_stub")
	siblingName := serverBinaryName
	if runtime.GOOS == "windows" {
		siblingName += ".exe"
		stubExe += ".exe"
	}
	pathServerBin := filepath.Join(pathDir, siblingName)
	if err := os.WriteFile(pathServerBin, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write path server: %v", err)
	}
	if err := os.Chmod(pathServerBin, 0o500); err != nil { //nolint:gosec // owner-only execute is intentional
		t.Fatalf("chmod path server: %v", err)
	}
	withStubExecutable(t, stubExe)
	withPATH(t, pathDir)

	got, err := findServerBinary()
	if err != nil {
		t.Fatalf("findServerBinary: %v", err)
	}
	gotR, _ := filepath.EvalSymlinks(got)
	wantR, _ := filepath.EvalSymlinks(pathServerBin)
	if gotR != wantR {
		t.Fatalf("findServerBinary returned %q, want %q", got, pathServerBin)
	}
}

func TestFindServerBinary_NotFound(t *testing.T) {
	tmp := t.TempDir()
	stubExe := filepath.Join(tmp, "stdio_stub")
	if runtime.GOOS == "windows" {
		stubExe += ".exe"
	}
	withStubExecutable(t, stubExe)
	withPATH(t, "")

	_, err := findServerBinary()
	if err == nil {
		t.Fatalf("findServerBinary returned nil error when no binary present")
	}
	var nf *ServerBinaryNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *ServerBinaryNotFoundError, got %T: %v", err, err)
	}
	if nf.SearchedDir != tmp {
		t.Fatalf("SearchedDir=%q, want %q", nf.SearchedDir, tmp)
	}
	if nf.LookPathErr == nil {
		t.Fatalf("LookPathErr is nil; want non-nil")
	}
	if !strings.Contains(err.Error(), tmp) {
		t.Fatalf("error message missing searched dir: %s", err.Error())
	}
}

func TestFindServerBinary_ExecutableErr(t *testing.T) {
	withStubExecutableErr(t, errors.New("simulated executable err"))
	withPATH(t, "")
	_, err := findServerBinary()
	var nf *ServerBinaryNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *ServerBinaryNotFoundError, got %v", err)
	}
	if nf.ExecutableErr == nil {
		t.Fatalf("ExecutableErr is nil; want non-nil")
	}
}

func TestWaitForServer_Healthy(t *testing.T) {
	port := pickFreePort(t)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	path, handler := knowledgev1connect.NewHealthServiceHandler(&fakeHealthHandler{})
	mux.Handle(path, handler)

	srv := &http.Server{
		Handler:           h2c.NewHandler(mux, &http2.Server{}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	if err := waitForServer(port, 3*time.Second); err != nil {
		t.Fatalf("waitForServer: %v", err)
	}
}

func TestWaitForServer_Timeout(t *testing.T) {
	port := pickFreePort(t)
	start := time.Now()
	err := waitForServer(port, 200*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("waitForServer returned nil for unbound port")
	}
	if !strings.Contains(err.Error(), "did not become healthy") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 150*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("unexpected elapsed %s (want roughly 200ms)", elapsed)
	}
}

func TestWaitForServer_TCPOnlyNoCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	err = waitForServer(port, 1*time.Second)
	if err == nil {
		t.Fatalf("waitForServer returned nil for TCP-only listener (no Check handler)")
	}
}

// fakeHealthHandler implements knowledgev1connect.HealthServiceHandler
// with a Check that always succeeds.
type fakeHealthHandler struct{}

func (fakeHealthHandler) Check(_ context.Context, _ *connect.Request[knowledgev1.HealthCheckRequest]) (*connect.Response[knowledgev1.HealthCheckResponse], error) {
	return connect.NewResponse(&knowledgev1.HealthCheckResponse{}), nil
}
func (fakeHealthHandler) Status(_ context.Context, _ *connect.Request[knowledgev1.StatusRequest]) (*connect.Response[knowledgev1.StatusResponse], error) {
	return connect.NewResponse(&knowledgev1.StatusResponse{}), nil
}

func pickFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close picked port: %v", err)
	}
	return port
}
