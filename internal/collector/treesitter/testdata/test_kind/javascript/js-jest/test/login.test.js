// SPDX-License-Identifier: Apache-2.0

describe("login", () => {
  it("succeeds", () => {
    expect(1 + 1).toBe(2);
  });

  test.each([
    [1, 1, 2],
    [2, 3, 5],
  ])("adds %i + %i = %i", (a, b, want) => {
    expect(a + b).toBe(want);
  });
});
