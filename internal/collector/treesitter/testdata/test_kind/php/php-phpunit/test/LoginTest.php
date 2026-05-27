<?php
// SPDX-License-Identifier: Apache-2.0

namespace Fixture;

use PHPUnit\Framework\TestCase;
use PHPUnit\Framework\Attributes\Test;

class LoginTest extends TestCase {
    public function testLogin(): void {
        $this->assertSame(2, 1 + 1);
    }

    #[Test]
    public function attributeForm(): void {
        $this->assertTrue(true);
    }
}
