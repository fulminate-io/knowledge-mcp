<?php
// SPDX-License-Identifier: Apache-2.0

namespace Fixture;

use PHPUnit\Framework\TestCase;

class LoginTest extends TestCase {
    /**
     * @dataProvider provideUsernames
     */
    public function withProvider(string $name): void {}

    public static function provideUsernames(): array {
        return [['alice'], ['bob']];
    }
}
