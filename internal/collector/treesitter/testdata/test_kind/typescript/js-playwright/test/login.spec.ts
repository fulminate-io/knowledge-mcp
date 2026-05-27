// SPDX-License-Identifier: Apache-2.0

import { test, expect } from "@playwright/test";

test.describe("login", () => {
  test("succeeds", async ({ page }) => {
    await page.goto("/");
    await expect(page).toHaveTitle(/Home/);
  });
});
