// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"os"
	"testing"
)

// errRealStoreInTest is returned by every real credential-store constructor
// when it is called from inside a test binary, enforcing the rule that tests
// must use in-memory fakes; the real credential store is off-limits to test
// binaries.
//
// A test that reached the real store would read and write the developer's own
// keychain: prompting them, leaving entries behind, and coupling the suite to
// whatever credential happens to be on the machine. The package's fakes
// (newTestStore and friends) are the only stores a test may use.
var errRealStoreInTest = errors.New(
	"auth: tests must use in-memory fakes; the real credential store is " +
		"off-limits to test binaries",
)

// homeAtStartup is the home directory as it stood when the process started.
//
// It is captured in a package-level variable precisely because package
// initialization runs BEFORE any test body, and therefore before any
// t.Setenv("HOME", ...). Comparing against it is how the file-store guard
// tells a test that redirected HOME to a temp directory (hermetic, allowed)
// from one that would write the developer's real credentials file (refused).
// A test cannot defeat the comparison by changing the environment, because
// the value it is compared against was fixed before the test existed.
var homeAtStartup = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}()

// refuseRealStoreInTest reports the rule violation when called from inside a
// test binary.
//
// testing.Testing() is compiled truth: it is true only in a binary built by
// `go test`, so no environment variable, build tag, or test setup can talk
// the guard out of firing.
func refuseRealStoreInTest() error {
	if testing.Testing() {
		return errRealStoreInTest
	}
	return nil
}

// refuseRealHomeInTest is the file-store half of the rule. The file store is
// legitimately constructed by hermetic tests that point HOME at a temp
// directory, so it is refused only when it would target the real home.
func refuseRealHomeInTest(home string) error {
	if testing.Testing() && homeAtStartup != "" && home == homeAtStartup {
		return errRealStoreInTest
	}
	return nil
}
