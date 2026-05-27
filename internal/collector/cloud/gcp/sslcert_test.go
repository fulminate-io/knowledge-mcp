// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSLCertificatesSubCollector_Name(t *testing.T) {
	c := &sslCertificatesSubCollector{}
	assert.Equal(t, "gcp-ssl-certificates", c.Name())
}

func TestSSLCertResourceSpec(t *testing.T) {
	selfLink := "https://www.googleapis.com/compute/v1/projects/p/global/sslCertificates/my-cert"

	t.Run("managed cert with domains", func(t *testing.T) {
		cert := &computepb.SslCertificate{
			SelfLink:   new(selfLink),
			Name:       new("my-cert"),
			Type:       new("MANAGED"),
			ExpireTime: new("2027-01-01T00:00:00Z"),
			Managed: &computepb.SslCertificateManagedSslCertificate{
				Status:  new("ACTIVE"),
				Domains: []string{"example.com", "www.example.com"},
			},
		}

		spec := sslCertResourceSpec(selfLink, cert)
		assert.Equal(t, selfLink, spec.ID)
		assert.Equal(t, "my-cert", spec.Name)
		assert.Equal(t, "gcp:compute:sslCertificate", spec.ResourceType)
		assert.Equal(t, "MANAGED", spec.Metadata["type"])
		assert.Equal(t, "ACTIVE", spec.Metadata["managed_status"])
		assert.Equal(t, "example.com,www.example.com", spec.Metadata["domains"])
		require.NotNil(t, spec.Content)
	})

	t.Run("self-managed cert", func(t *testing.T) {
		cert := &computepb.SslCertificate{
			SelfLink:   new(selfLink),
			Name:       new("self-cert"),
			Type:       new("SELF_MANAGED"),
			ExpireTime: new("2027-06-01T00:00:00Z"),
			// No managed block.
		}

		spec := sslCertResourceSpec(selfLink, cert)
		assert.Equal(t, "SELF_MANAGED", spec.Metadata["type"])
		assert.Empty(t, spec.Metadata["managed_status"])
	})

	t.Run("content does not contain privateKey", func(t *testing.T) {
		cert := &computepb.SslCertificate{
			SelfLink:   new(selfLink),
			Name:       new("sec-cert"),
			PrivateKey: new("-----BEGIN RSA PRIVATE KEY-----\nMIIE..."),
		}

		spec := sslCertResourceSpec(selfLink, cert)
		assert.NotContains(t, string(spec.Content), "PRIVATE KEY")
	})
}
