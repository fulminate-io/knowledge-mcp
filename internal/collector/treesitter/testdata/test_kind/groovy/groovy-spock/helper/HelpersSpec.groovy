// SPDX-License-Identifier: Apache-2.0

package fixture

import spock.lang.Specification

// Helper methods inside a Spec class — neither setup/teardown name nor
// recognized — classify as TestKindHelper.
class HelpersSpec extends Specification {
    def fixtureFor(String name) { "fixture-${name}" }

    def utility() { 42 }
}
