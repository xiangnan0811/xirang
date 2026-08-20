import { describe, expect, it } from "vitest";

import { finiteInteger } from "./lifecycle-integers";

describe("finiteInteger", () => {
  it("accepts safe integers and numeric strings", () => {
    expect(finiteInteger(0)).toBe(0);
    expect(finiteInteger(12)).toBe(12);
    expect(finiteInteger("7")).toBe(7);
    expect(finiteInteger(" 8 ")).toBe(8);
  });

  it("rejects non-numeric types and blank strings", () => {
    expect(finiteInteger(null)).toBeNull();
    expect(finiteInteger(false)).toBeNull();
    expect(finiteInteger("")).toBeNull();
    expect(finiteInteger("   ")).toBeNull();
    expect(finiteInteger(undefined)).toBeNull();
    expect(finiteInteger({})).toBeNull();
    expect(finiteInteger([])).toBeNull();
    expect(finiteInteger(1.5)).toBeNull();
    expect(finiteInteger("1.5")).toBeNull();
  });
});
