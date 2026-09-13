// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "os"

// bootstrapHomeDir resolves standalone installation defaults. Tests inject
// scratch directories without changing HOME or the shared toolchain cache.
var bootstrapHomeDir = os.UserHomeDir
