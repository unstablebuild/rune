import { expect, test } from "bun:test";
import { add } from "./math.ts";

test("add sums its operands", () => {
  expect(add(2, 3)).toBe(5);
});
