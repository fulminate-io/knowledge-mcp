<?php
// SPDX-License-Identifier: Apache-2.0

namespace Fixture;

class LoginTest {
    public function makeUser(string $name): string {
        return 'user-' . $name;
    }
}
