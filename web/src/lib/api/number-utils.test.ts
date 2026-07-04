import { describe, expect, it } from "vitest";
import { finiteNumber, nullableFiniteNumber, positiveNumberOrUndefined } from "./number-utils";

describe("api number utils", () => {
  it("returns finite numbers and falls back for non-finite values", () => {
    expect(finiteNumber("42")).toBe(42);
    expect(finiteNumber(3.5)).toBe(3.5);
    expect(finiteNumber("bad")).toBe(0);
    expect(finiteNumber(undefined, 7)).toBe(7);
    expect(finiteNumber(Number.NaN, 9)).toBe(9);
  });

  it("returns only positive finite numbers as optional values", () => {
    expect(positiveNumberOrUndefined("42")).toBe(42);
    expect(positiveNumberOrUndefined(0)).toBeUndefined();
    expect(positiveNumberOrUndefined("-1")).toBeUndefined();
    expect(positiveNumberOrUndefined("bad")).toBeUndefined();
    expect(positiveNumberOrUndefined(undefined)).toBeUndefined();
  });

  it("returns null for missing, empty, or invalid nullable numbers", () => {
    expect(nullableFiniteNumber("42")).toBe(42);
    expect(nullableFiniteNumber(0)).toBe(0);
    expect(nullableFiniteNumber(null)).toBeNull();
    expect(nullableFiniteNumber(undefined)).toBeNull();
    expect(nullableFiniteNumber("")).toBeNull();
    expect(nullableFiniteNumber("bad")).toBeNull();
  });
});
