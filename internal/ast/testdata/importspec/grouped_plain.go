// SPDX-License-Identifier: Apache-2.0

// Fixture: the GROUPED PLAIN spec form, alongside a sibling spec so the group
// carries more than one entry. Not compiled — testdata.
package importspec

import (
	"fmt"
	"os/exec"
)

var _ = fmt.Sprint
var _ = exec.Command
