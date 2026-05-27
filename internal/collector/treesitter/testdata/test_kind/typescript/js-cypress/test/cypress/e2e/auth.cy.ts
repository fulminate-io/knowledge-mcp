// SPDX-License-Identifier: Apache-2.0

describe("auth", () => {
  it("logs in", () => {
    cy.visit("/login");
    cy.contains("Sign in");
  });
});
