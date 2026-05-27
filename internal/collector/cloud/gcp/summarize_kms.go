// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:cloudkms:keyRing", summarizeKMSKeyRing)
	cloud.Register("gcp:cloudkms:cryptoKey", summarizeKMSCryptoKey)
}

func summarizeKMSKeyRing(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud KMS key ring", spec)
}

func summarizeKMSCryptoKey(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud KMS crypto key", spec)
}
