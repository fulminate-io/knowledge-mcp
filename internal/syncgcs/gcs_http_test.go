// SPDX-License-Identifier: Apache-2.0

package syncgcs

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPutObjectNoAuthHeader asserts PutObject issues a plain PUT with the signed
// content-type and NO Authorization header (a Bearer would break the GCS V4
// signature — the regression guard against accidentally routing through
// auth.Transport).
func TestPutObjectNoAuthHeader(t *testing.T) {
	payload := []byte("ciphertext-blob")
	var gotAuth, gotCT, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := PutObject(context.Background(), srv.URL, payload, "application/octet-stream"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("PUT carried Authorization header %q, want none", gotAuth)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotCT != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", gotCT)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Errorf("body = %q, want %q", gotBody, payload)
	}
}

// TestGetObjectNoAuthHeader asserts GetObject issues a plain GET with NO
// Authorization header and returns the body bytes.
func TestGetObjectNoAuthHeader(t *testing.T) {
	payload := []byte("downloaded-ciphertext")
	var gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	got, err := GetObject(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("GET carried Authorization header %q, want none", gotAuth)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", gotMethod)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("body = %q, want %q", got, payload)
	}
}

func TestPutObjectSurfacesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("SignatureDoesNotMatch"))
	}))
	defer srv.Close()
	if err := PutObject(context.Background(), srv.URL, []byte("x"), "application/octet-stream"); err == nil {
		t.Fatal("PutObject on 403 should error")
	}
}

func TestGetObjectSurfacesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := GetObject(context.Background(), srv.URL); err == nil {
		t.Fatal("GetObject on 404 should error")
	}
}
