// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from "@jest/globals";
import { render } from "@testing-library/react";

// JSX-bearing component: the plain typescript grammar derails on <div>; the
// tsx grammar parses it cleanly. Present so the fixture exercises real JSX.
function Greeting({ name }: { name: string }) {
  return <div className="greeting">Hello, {name}!</div>;
}

describe("Greeting", () => {
  it("renders the name", () => {
    const { container } = render(<Greeting name="world" />);
    expect(container.textContent).toBe("Hello, world!");
  });
});
