// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:monitoring:alertPolicy", summarizeAlertPolicy)
	cloud.Register("gcp:monitoring:notificationChannel", summarizeNotificationChannel)
}

func summarizeAlertPolicy(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("monitoring alert policy", spec)
}

func summarizeNotificationChannel(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("notification channel", spec)
}
