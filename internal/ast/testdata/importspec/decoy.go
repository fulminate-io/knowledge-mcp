// SPDX-License-Identifier: Apache-2.0

// THE DECOY, and it is the whole point of this corpus: the string "os/exec"
// appears twice as an ordinary value and the file imports nothing. An import
// pattern must not match either occurrence, while a bare string-literal
// pattern matches both — that difference is what separates a structural
// import form from a text search. Not compiled — testdata.
package importspec

// SpawnPackage and NamePackage are EXPORTED so the fixture typechecks under
// the repository's hygiene pass; nothing reads them and nothing needs to.
const SpawnPackage = "os/exec"

func NamePackage() string {
	return "os/exec"
}
