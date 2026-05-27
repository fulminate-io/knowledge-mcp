// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"strings"
	"testing"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// streamWithLabels builds a wirelogs.LogStream populated only with the labels
// needed for alias derivation. ID/Fingerprint are intentionally left
// empty — AliasFor must derive solely from labels.
func streamWithLabels(labels map[string]string) *wirelogs.LogStream {
	return &wirelogs.LogStream{Labels: labels}
}

func TestAliasFor_K8s_PodReason(t *testing.T) {
	s := streamWithLabels(map[string]string{
		"pod_name": "anetd-l-fb2x9",
		"reason":   "Unhealthy",
		"kind":     "Pod",
	})
	got := AliasFor(s)
	want := "anetd-l-fb2x9.Unhealthy"
	if got != want {
		t.Errorf("AliasFor pod+reason = %q, want %q", got, want)
	}
}

func TestAliasFor_K8s_ServiceFallback(t *testing.T) {
	// No pod_name; service is the next preference.
	s := streamWithLabels(map[string]string{
		"service": "fluent-bit",
		"reason":  "FailedScheduling",
		"kind":    "Pod",
	})
	got := AliasFor(s)
	want := "fluent-bit.FailedScheduling"
	if got != want {
		t.Errorf("AliasFor service fallback = %q, want %q", got, want)
	}
}

func TestAliasFor_K8s_KindFallback(t *testing.T) {
	// Neither pod_name nor service present → kind is last resort.
	s := streamWithLabels(map[string]string{
		"kind":   "Node",
		"reason": "NotReady",
	})
	got := AliasFor(s)
	want := "Node.NotReady"
	if got != want {
		t.Errorf("AliasFor kind fallback = %q, want %q", got, want)
	}
}

func TestAliasFor_Loki_AppInstance(t *testing.T) {
	s := streamWithLabels(map[string]string{
		"app":      "checkout",
		"instance": "host-3",
		"job":      "checkout-prod", // must not appear in alias
	})
	got := AliasFor(s)
	want := "checkout@host-3"
	if got != want {
		t.Errorf("AliasFor loki = %q, want %q", got, want)
	}
	if strings.Contains(got, "job") || strings.Contains(got, "checkout-prod") {
		t.Errorf("AliasFor must not include job: got %q", got)
	}
}

func TestAliasFor_Loki_AppOnly(t *testing.T) {
	// Loki without instance: alias collapses to just the app.
	s := streamWithLabels(map[string]string{"app": "api"})
	got := AliasFor(s)
	want := "api"
	if got != want {
		t.Errorf("AliasFor loki app-only = %q, want %q", got, want)
	}
}

func TestAliasFor_Stackdriver_HostResource(t *testing.T) {
	s := streamWithLabels(map[string]string{
		"host":          "api-7b6",
		"resource_type": "k8s_container",
	})
	got := AliasFor(s)
	want := "api-7b6.k8s_container"
	if got != want {
		t.Errorf("AliasFor stackdriver = %q, want %q", got, want)
	}
}

func TestAliasFor_Stackdriver_PodNameFallback(t *testing.T) {
	// No host label; pod_name is the next preference.
	s := streamWithLabels(map[string]string{
		"pod_name":      "checkout-x9",
		"resource_type": "k8s_pod",
	})
	got := AliasFor(s)
	want := "checkout-x9.k8s_pod"
	if got != want {
		t.Errorf("AliasFor stackdriver pod fallback = %q, want %q", got, want)
	}
}

func TestAliasFor_CloudWatch_StreamService(t *testing.T) {
	s := streamWithLabels(map[string]string{
		"log_stream": "ecs-task-1234",
		"service":    "api-server",
		"log_group":  "/ecs/prod/api-server",
	})
	got := AliasFor(s)
	want := "ecs-task-1234.api-server"
	if got != want {
		t.Errorf("AliasFor cloudwatch = %q, want %q", got, want)
	}
}

func TestAliasFor_Generic_TwoLabels(t *testing.T) {
	// Unrecognized shape — first two label keys alphabetically.
	s := streamWithLabels(map[string]string{
		"region":  "us-west-2",
		"account": "123456789012",
		"env":     "prod",
	})
	got := AliasFor(s)
	// Sorted: account, env, region → take account + env.
	want := "account=123456789012.env=prod"
	if got != want {
		t.Errorf("AliasFor generic = %q, want %q", got, want)
	}
}

func TestAliasFor_PreservesCase(t *testing.T) {
	// The most important promise: OOMKilled stays OOMKilled.
	s := streamWithLabels(map[string]string{
		"pod_name": "kube-proxy-Master",
		"reason":   "OOMKilled",
	})
	got := AliasFor(s)
	if !strings.Contains(got, "OOMKilled") {
		t.Errorf("AliasFor must preserve case: got %q, expected OOMKilled token", got)
	}
	if !strings.Contains(got, "kube-proxy-Master") {
		t.Errorf("AliasFor must preserve case: got %q, expected kube-proxy-Master token", got)
	}
}

func TestAliasFor_SanitizesPunctuation(t *testing.T) {
	// Slashes, spaces, and control characters collapse to `-`.
	s := streamWithLabels(map[string]string{
		"pod_name": "weird/name with spaces",
		"reason":   "Failed to do thing",
	})
	got := AliasFor(s)
	for _, r := range got {
		switch r {
		case '/', ' ', '\t':
			t.Errorf("AliasFor must sanitize unsafe chars: got %q (contains %q)", got, r)
		}
	}
	// Separators are still meaningful.
	if !strings.Contains(got, ".") {
		t.Errorf("AliasFor must keep `.` separator: got %q", got)
	}
}

func TestAliasFor_Empty(t *testing.T) {
	if got := AliasFor(nil); got != "" {
		t.Errorf("AliasFor(nil) = %q, want empty", got)
	}
	if got := AliasFor(&wirelogs.LogStream{}); got != "" {
		t.Errorf("AliasFor(empty stream) = %q, want empty", got)
	}
}

func TestAliasFor_FallsBackToLowCardLabels(t *testing.T) {
	// When Labels is empty, LowCardLabels is the fallback source.
	s := &wirelogs.LogStream{
		LowCardLabels: map[string]string{
			"pod_name": "api-7b6",
			"reason":   "OOMKilled",
		},
	}
	got := AliasFor(s)
	want := "api-7b6.OOMKilled"
	if got != want {
		t.Errorf("AliasFor lowcard fallback = %q, want %q", got, want)
	}
}

func TestTemplateAliasFor_KebabCase(t *testing.T) {
	tpl := &wirelogs.LogTemplate{Pattern: "Node is not ready", Severity: wirelogs.SeverityError}
	got := TemplateAliasFor(tpl)
	// `is` is a stopword; tokens become [node, not, ready].
	want := "node-not-ready@err"
	if got != want {
		t.Errorf("TemplateAliasFor kebab = %q, want %q", got, want)
	}
}

func TestTemplateAliasFor_StripsWildcards(t *testing.T) {
	tpl := &wirelogs.LogTemplate{Pattern: "Failed to mount <*> at <*>", Severity: wirelogs.SeverityError}
	got := TemplateAliasFor(tpl)
	// `to` is a stopword; `at` is NOT in the stopword list (kept short
	// to avoid dropping meaningful tokens). Wildcards are stripped.
	want := "failed-mount-at@err"
	if got != want {
		t.Errorf("TemplateAliasFor wildcards = %q, want %q", got, want)
	}
}

func TestTemplateAliasFor_StripsAllStopwords(t *testing.T) {
	stopwords := []string{"a", "an", "the", "of", "on", "for", "to", "in", "is", "and", "or", "with"}
	for _, sw := range stopwords {
		pattern := "real " + sw + " token"
		tpl := &wirelogs.LogTemplate{Pattern: pattern, Severity: wirelogs.SeverityInfo}
		got := TemplateAliasFor(tpl)
		want := "real-token@info"
		if got != want {
			t.Errorf("stopword %q: TemplateAliasFor(%q) = %q, want %q", sw, pattern, got, want)
		}
	}
}

func TestTemplateAliasFor_TakesFirstFiveTokens(t *testing.T) {
	tpl := &wirelogs.LogTemplate{
		Pattern:  "alpha bravo charlie delta echo foxtrot golf hotel",
		Severity: wirelogs.SeverityWarn,
	}
	got := TemplateAliasFor(tpl)
	want := "alpha-bravo-charlie-delta-echo@warn"
	if got != want {
		t.Errorf("TemplateAliasFor max-tokens = %q, want %q", got, want)
	}
}

func TestTemplateAliasFor_SeverityShortSuffix(t *testing.T) {
	tests := []struct {
		sev  string
		want string
	}{
		{wirelogs.SeverityCritical, "node-down@crit"},
		{wirelogs.SeverityError, "node-down@err"},
		{wirelogs.SeverityWarn, "node-down@warn"},
		{wirelogs.SeverityInfo, "node-down@info"},
		{wirelogs.SeverityDebug, "node-down@debug"},
		{wirelogs.SeverityTrace, "node-down@trace"},
		{"NOVEL", "node-down@novel"}, // unknown severity → lowercased.
		{"", "node-down"},            // missing severity → no suffix.
	}
	for _, tc := range tests {
		tpl := &wirelogs.LogTemplate{Pattern: "node down", Severity: tc.sev}
		if got := TemplateAliasFor(tpl); got != tc.want {
			t.Errorf("severity %q: TemplateAliasFor = %q, want %q", tc.sev, got, tc.want)
		}
	}
}

func TestTemplateAliasFor_Empty(t *testing.T) {
	if got := TemplateAliasFor(nil); got != "" {
		t.Errorf("TemplateAliasFor(nil) = %q, want empty", got)
	}
	if got := TemplateAliasFor(&wirelogs.LogTemplate{}); got != "" {
		t.Errorf("TemplateAliasFor(empty) = %q, want empty", got)
	}
	if got := TemplateAliasFor(&wirelogs.LogTemplate{Pattern: "<*>"}); got != "" {
		t.Errorf("TemplateAliasFor wildcard-only = %q, want empty", got)
	}
}

func TestTemplateAliasFor_Deterministic(t *testing.T) {
	// Same input must produce same output every call.
	tpl := &wirelogs.LogTemplate{Pattern: "Pod <*> evicted", Severity: wirelogs.SeverityWarn}
	first := TemplateAliasFor(tpl)
	for i := range 10 {
		if got := TemplateAliasFor(tpl); got != first {
			t.Fatalf("non-deterministic: iter %d got %q, first was %q", i, got, first)
		}
	}
}

// TestTemplateAliasFor_StripsK8sReasonPrefix asserts the polish fix:
// K8s events whose template pattern is structured "<Reason>: <msg>"
// drop the reason prefix from the alias body (the reason is already
// in the stream's `reason` label, so duplicating it is noise).
func TestTemplateAliasFor_StripsK8sReasonPrefix(t *testing.T) {
	tests := []struct {
		pattern string
		sev     string
		want    string
	}{
		{"NodeNotReady: Node is not ready", wirelogs.SeverityError, "node-not-ready@err"},
		{"NetworkNotReady: network is not ready: container runtime", wirelogs.SeverityError,
			"network-not-ready-container-runtime@err"},
		{"FailedMount: MountVolume.SetUp failed for volume", wirelogs.SeverityError,
			"mountvolume-setup-failed-volume@err"},
		{"OOMKilled: container <*> killed", wirelogs.SeverityError, "container-killed@err"},
	}
	for _, tc := range tests {
		tpl := &wirelogs.LogTemplate{Pattern: tc.pattern, Severity: tc.sev}
		got := TemplateAliasFor(tpl)
		if got != tc.want {
			t.Errorf("pattern %q: TemplateAliasFor = %q, want %q", tc.pattern, got, tc.want)
		}
	}
}

// TestTemplateAliasFor_DoesNotStripNonReason asserts the prefix
// stripper only fires for the strict K8s "<CamelCase>: " shape — not
// arbitrary "x: y" patterns.
func TestTemplateAliasFor_DoesNotStripNonReason(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		// "starting:" is lowercase — must not be stripped.
		{"starting: kubelet service", "starting-kubelet-service"},
		// Multiple words before the colon — must not be stripped.
		{"Network plugin: error initializing", "network-plugin-error-initializing"},
		// CamelCase but with a digit — must not be stripped (letters only).
		{"Code400: bad request", "code400-bad-request"},
	}
	for _, tc := range tests {
		tpl := &wirelogs.LogTemplate{Pattern: tc.pattern}
		got := TemplateAliasFor(tpl)
		if got != tc.want {
			t.Errorf("pattern %q: TemplateAliasFor = %q, want %q", tc.pattern, got, tc.want)
		}
	}
}

// TestStripReasonPrefix_EdgeCases pins boundary behavior for the
// strip helper directly.
func TestStripReasonPrefix_EdgeCases(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"", ""},
		{": no prefix", ": no prefix"}, // empty prefix
		{"NoSpace:right after", "NoSpace:right after"}, // no ": " (space required)
		{"X: short", "X: short"},                       // single-char prefix is rejected
		{"NodeNotReady: ok", "ok"},                     // canonical
		{"NodeNotReady: ", "NodeNotReady: "},           // empty body after prefix
	}
	for _, c := range cases {
		if got := stripReasonPrefix(c.in); got != c.out {
			t.Errorf("stripReasonPrefix(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

func TestShortHash_Length(t *testing.T) {
	full := "abcdef0123456789deadbeef"
	if got := ShortHash(full); got != "abcdef01" {
		t.Errorf("ShortHash long = %q, want abcdef01", got)
	}
	short := "abc"
	if got := ShortHash(short); got != "abc" {
		t.Errorf("ShortHash short = %q, want abc", got)
	}
}
