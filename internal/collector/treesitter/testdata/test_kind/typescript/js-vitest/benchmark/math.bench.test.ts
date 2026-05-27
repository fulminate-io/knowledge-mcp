// SPDX-License-Identifier: Apache-2.0

import { bench } from "vitest";

bench("parse", () => {
  JSON.parse('{"x":1}');
});
