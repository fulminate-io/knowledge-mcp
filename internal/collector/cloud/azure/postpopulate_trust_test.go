// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsCrossTenantPrincipal(t *testing.T) {
	assert.True(t, isCrossTenantPrincipal("Guest"))
	assert.True(t, isCrossTenantPrincipal("ForeignGroup"))
	assert.False(t, isCrossTenantPrincipal("ServicePrincipal"))
	assert.False(t, isCrossTenantPrincipal("User"))
	assert.False(t, isCrossTenantPrincipal(""))
}

func TestExtractTenantFromIssuer(t *testing.T) {
	t.Run("standard Azure AD OIDC issuer", func(t *testing.T) {
		issuer := "https://login.microsoftonline.com/tenant-abc-123/v2.0"
		assert.Equal(t, "tenant-abc-123", extractTenantFromIssuer(issuer))
	})

	t.Run("no v2.0 suffix", func(t *testing.T) {
		issuer := "https://login.microsoftonline.com/tenant-xyz"
		assert.Equal(t, "tenant-xyz", extractTenantFromIssuer(issuer))
	})

	t.Run("not an Azure AD issuer", func(t *testing.T) {
		issuer := "https://oidc.example.com/tenant-id"
		assert.Empty(t, extractTenantFromIssuer(issuer))
	})

	t.Run("empty", func(t *testing.T) {
		assert.Empty(t, extractTenantFromIssuer(""))
	})
}

func TestIsExternalIssuer(t *testing.T) {
	const myTenant = "my-tenant-id"

	t.Run("same tenant Azure AD", func(t *testing.T) {
		issuer := "https://login.microsoftonline.com/my-tenant-id/v2.0"
		assert.False(t, isExternalIssuer(issuer, myTenant))
	})

	t.Run("different tenant Azure AD", func(t *testing.T) {
		issuer := "https://login.microsoftonline.com/other-tenant-id/v2.0"
		assert.True(t, isExternalIssuer(issuer, myTenant))
	})

	t.Run("AKS issuer is not external", func(t *testing.T) {
		issuer := "https://eastus.oic.prod-aks.azure.com/my-tenant-id/cluster-1/"
		assert.False(t, isExternalIssuer(issuer, myTenant))
	})

	t.Run("GitHub Actions issuer is not external", func(t *testing.T) {
		issuer := "https://token.actions.githubusercontent.com"
		assert.False(t, isExternalIssuer(issuer, myTenant))
	})

	t.Run("generic OIDC issuer is external", func(t *testing.T) {
		issuer := "https://oidc.partner-company.com"
		assert.True(t, isExternalIssuer(issuer, myTenant))
	})

	t.Run("empty tenant skips Azure AD check", func(t *testing.T) {
		// Even though it's login.microsoftonline.com, if we can't
		// extract a tenant, we need to be safe — don't flag.
		issuer := "https://login.microsoftonline.com/"
		assert.False(t, isExternalIssuer(issuer, myTenant))
	})
}

func TestParseEdgeMetadata(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		md := parseEdgeMetadata(`{"principal_type":"Guest","source":"rbac"}`)
		assert.Equal(t, "Guest", md["principal_type"])
		assert.Equal(t, "rbac", md["source"])
	})

	t.Run("empty", func(t *testing.T) {
		assert.Nil(t, parseEdgeMetadata(""))
	})

	t.Run("invalid JSON", func(t *testing.T) {
		assert.Nil(t, parseEdgeMetadata("{invalid"))
	})
}
