// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	dns "google.golang.org/api/dns/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDNSSubCollector_Name(t *testing.T) {
	c := &dnsSubCollector{}
	assert.Equal(t, "gcp-cloud-dns", c.Name())
}

func TestDNSZoneSpec(t *testing.T) {
	zone := &dns.ManagedZone{
		Name:       "my-zone",
		DnsName:    "example.com.",
		Visibility: "public",
	}
	spec, err := dnsZoneSpec("my-project", zone)
	require.NoError(t, err)
	assert.Equal(t, "projects/my-project/managedZones/my-zone", spec.ID)
	assert.Equal(t, "my-zone", spec.Name)
	assert.Equal(t, "gcp:dns:managedZone", spec.ResourceType)
	assert.Equal(t, "example.com.", spec.Metadata["dnsName"])
	assert.Equal(t, "public", spec.Metadata["visibility"])
}

func TestDNSRecordSpec(t *testing.T) {
	rs := &dns.ResourceRecordSet{
		Name:    "app.example.com.",
		Type:    "A",
		Ttl:     300,
		Rrdatas: []string{"10.0.0.1", "10.0.0.2"},
	}
	spec, err := dnsRecordSpec("projects/p/managedZones/z", rs)
	require.NoError(t, err)
	assert.Equal(t, "projects/p/managedZones/z/rrsets/app.example.com./A", spec.ID)
	assert.Equal(t, "app.example.com.", spec.Name)
	assert.Equal(t, "gcp:dns:recordSet", spec.ResourceType)
	assert.Equal(t, "A", spec.Metadata["type"])
	assert.Equal(t, "300", spec.Metadata["ttl"])
	assert.Equal(t, "10.0.0.1,10.0.0.2", spec.Metadata["rrdatas"])
}
