// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_GetPaginated_MultiplePages(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")

		switch page {
		case 1:
			body := fmt.Sprintf(`{"values":["a","b"],"next":"http://%s/page2"}`, r.Host)
			_, _ = w.Write([]byte(body)) //nolint:gosec // test handler
		case 2:
			body := fmt.Sprintf(`{"values":["c"],"next":"http://%s/page3"}`, r.Host)
			_, _ = w.Write([]byte(body)) //nolint:gosec // test handler
		default:
			_, _ = w.Write([]byte(`{"values":["d"]}`))
		}
	}))
	defer srv.Close()

	c := NewClient("user", "pass")
	c.baseURL = srv.URL

	var all []string
	err := c.GetPaginated(context.Background(), "/items", func(raw json.RawMessage) error {
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		all = append(all, items...)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c", "d"}, all)
	assert.Equal(t, 3, page)
}

func TestClient_GetPaginated_SinglePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":["only"]}`))
	}))
	defer srv.Close()

	c := NewClient("user", "pass")
	c.baseURL = srv.URL

	var all []string
	err := c.GetPaginated(context.Background(), "/single", func(raw json.RawMessage) error {
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		all = append(all, items...)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"only"}, all)
}

func TestClient_RateLimitRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient("user", "pass")
	c.baseURL = srv.URL

	body, err := c.GetRaw(context.Background(), "/rate-limited")
	require.NoError(t, err)
	assert.Contains(t, string(body), `"ok":true`)
	assert.Equal(t, 2, attempts)
}

func TestClient_GetRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("pipelines:\n  default:\n    - step:\n        name: test"))
	}))
	defer srv.Close()

	c := NewClient("user", "pass")
	c.baseURL = srv.URL

	data, err := c.GetRaw(context.Background(), "/raw-file")
	require.NoError(t, err)
	assert.Contains(t, string(data), "pipelines:")
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"", "2s"},
		{"5", "5s"},
		{"0", "1s"},
		{"-1", "1s"},
		{"abc", "2s"},
	}
	for _, tt := range tests {
		got := parseRetryAfter(tt.header)
		assert.Equal(t, tt.want, got.String(), "header=%q", tt.header)
	}
}
