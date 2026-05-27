// SPDX-License-Identifier: Apache-2.0

import { test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await page.goto("/");
});

test.beforeAll(async () => {});
