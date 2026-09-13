// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type storageClosingTransport struct {
	http.RoundTripper
	closed bool
}

func (t *storageClosingTransport) CloseIdleConnections() { t.closed = true }

func TestStorageCloudClientReleasesTransport(t *testing.T) {
	base := &storageClosingTransport{}
	c := &GraphClient{httpClient: &http.Client{Transport: &bearerRoundTripper{base: base}}}
	c.Close()
	require.True(t, base.closed)
}
