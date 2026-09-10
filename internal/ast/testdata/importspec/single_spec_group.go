// SPDX-License-Identifier: Apache-2.0

// Fixture: a group holding exactly ONE spec. This is the form a
// parenthesized single-spec pattern already matched before the import
// wrapper existed, and it is here so the wrapper's five-form claim is
// measured against it rather than assumed. Not compiled — testdata.
package importspec

import (
	"os/exec"
)

var _ = exec.Command
