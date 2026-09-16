// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// namespaceEnv describes how the selector is presented to a test: absent from
// the environment entirely, or present carrying a value. The two are different
// inputs rather than one nullable one — an absence is today's behaviour and a
// present-but-empty value is a refusal — so the distinction is carried in a
// field instead of in the nil-ness of a pointer.
type namespaceEnv struct {
	set bool
	raw string
}

func namespaceUnset() namespaceEnv         { return namespaceEnv{} }
func namespaceSet(raw string) namespaceEnv { return namespaceEnv{set: true, raw: raw} }

// describe renders the input for a failure message, so a row that fails says
// which of the two presentations it was driving.
func (n namespaceEnv) describe() string {
	if !n.set {
		return "<unset>"
	}
	return n.raw
}

// setNamespaceEnv presents the selector to the process for the duration of the
// test. t.Setenv registers the restore in both arms, so the unset row cannot
// leak into a sibling test.
func setNamespaceEnv(t *testing.T, env namespaceEnv) {
	t.Helper()
	t.Setenv(CredentialNamespaceEnv, "placeholder-restored-by-cleanup")
	if !env.set {
		if err := os.Unsetenv(CredentialNamespaceEnv); err != nil {
			t.Fatalf("unset %s: %v", CredentialNamespaceEnv, err)
		}
		return
	}
	t.Setenv(CredentialNamespaceEnv, env.raw)
}

// TestCredentialNamespace_ValueParsing pins the accepted shape one row per
// input class. Every refusal row asserts the SENTINEL rather than the message,
// so a reworded refusal does not silently pass.
func TestCredentialNamespace_ValueParsing(t *testing.T) {
	cases := []struct {
		name    string
		raw     namespaceEnv
		want    string
		refused bool
	}{
		{name: "unset is an absence, not a refusal", raw: namespaceUnset(), want: ""},
		{name: "set but empty is refused", raw: namespaceSet(""), refused: true},
		{name: "a single space is refused", raw: namespaceSet(" "), refused: true},
		{name: "a tab is refused", raw: namespaceSet("\t"), refused: true},
		{name: "a padded value is refused rather than trimmed", raw: namespaceSet(" dev "), refused: true},
		{name: "dev is accepted", raw: namespaceSet("dev"), want: "dev"},
		{name: "one letter is accepted", raw: namespaceSet("a"), want: "a"},
		{name: "one digit is accepted", raw: namespaceSet("0"), want: "0"},
		{name: "inner dashes are accepted", raw: namespaceSet("a-b-c"), want: "a-b-c"},
		{name: "32 characters is the boundary and is accepted", raw: namespaceSet(strings.Repeat("a", 32)), want: strings.Repeat("a", 32)},
		{name: "33 characters is beyond the boundary and is refused", raw: namespaceSet(strings.Repeat("a", 33)), refused: true},
		{name: "a leading dash is refused", raw: namespaceSet("-dev"), refused: true},
		{name: "an uppercase letter is refused", raw: namespaceSet("Dev"), refused: true},
		{name: "an underscore is refused", raw: namespaceSet("dev_1"), refused: true},
		{name: "a dot is refused", raw: namespaceSet("dev.1"), refused: true},
		{name: "a slash is refused", raw: namespaceSet("dev/1"), refused: true},
		{name: "a trailing newline is refused", raw: namespaceSet("dev\n"), refused: true},
		{name: "the default service name is refused", raw: namespaceSet("io.fulminate.knowledge"), refused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setNamespaceEnv(t, tc.raw)
			got, err := CredentialNamespace()
			if tc.refused {
				if !errors.Is(err, ErrCredentialNamespaceInvalid) {
					t.Fatalf("CredentialNamespace() with %q = (%q, %v), want ErrCredentialNamespaceInvalid", tc.raw.describe(), got, err)
				}
				if got != "" {
					t.Errorf("CredentialNamespace() returned namespace %q alongside a refusal — a refused value must never reach a store", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CredentialNamespace() with %q = (%q, %v), want no error", tc.raw.describe(), got, err)
			}
			if got != tc.want {
				t.Errorf("CredentialNamespace() with %q = %q, want %q", tc.raw.describe(), got, tc.want)
			}
		})
	}
}

// TestCredentialNamespace_RefusalNamesTheVariableAndTheShape pins the half of
// the refusal a human reads: the Desktop surfaces this text on stderr and
// `doctor` renders it, so it must say which variable is wrong and what would
// have been accepted. It also pins that the offending value is echoed, which
// is what makes a typo self-evident.
func TestCredentialNamespace_RefusalNamesTheVariableAndTheShape(t *testing.T) {
	setNamespaceEnv(t, namespaceSet("Dev"))
	_, err := CredentialNamespace()
	if err == nil {
		t.Fatal("CredentialNamespace() with \"Dev\" returned no error")
	}
	msg := err.Error()
	for _, want := range []string{CredentialNamespaceEnv, credentialNamespaceShape, `"Dev"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not name %q", msg, want)
		}
	}
	if strings.HasSuffix(msg, ".") {
		t.Errorf("refusal %q ends with punctuation", msg)
	}
	if u := strings.ToUpper(msg[:1]); msg[:1] == u && u != strings.ToLower(msg[:1]) {
		t.Errorf("refusal %q is capitalized", msg)
	}
}

// TestCredentialServiceName pins the derived keychain service name against a
// literal computed in the test, never by calling the production deriver on
// both sides of the comparison.
func TestCredentialServiceName(t *testing.T) {
	if got := credentialServiceName(""); got != ServiceName {
		t.Errorf("credentialServiceName(\"\") = %q, want the default service %q", got, ServiceName)
	}
	if got, want := credentialServiceName("dev"), "io.fulminate.knowledge.dev"; got != want {
		t.Errorf("credentialServiceName(%q) = %q, want %q", "dev", got, want)
	}
}

// TestCredentialsFileNameFor pins the file-store basename the same way.
func TestCredentialsFileNameFor(t *testing.T) {
	if got := credentialsFileNameFor(""); got != credentialsFileName {
		t.Errorf("credentialsFileNameFor(\"\") = %q, want the default basename %q", got, credentialsFileName)
	}
	if got, want := credentialsFileNameFor("dev"), "credentials.dev"; got != want {
		t.Errorf("credentialsFileNameFor(%q) = %q, want %q", "dev", got, want)
	}
}
