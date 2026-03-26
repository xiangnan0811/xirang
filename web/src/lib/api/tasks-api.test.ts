import { describe, expect, it } from "vitest";
import { deriveTaskProgress } from "./tasks-api";

describe("deriveTaskProgress", () => {
  it("apiProgress 为 0 时返回 0（有活跃 run 但尚无进度样本）", () => {
    // restore 刚启动：Task.status=success，后端返回 progress=0
    expect(deriveTaskProgress("success", 0, 0, 0)).toBe(0);
  });

  it("apiProgress 为正整数时直接返回（restore 进行中）", () => {
    expect(deriveTaskProgress("success", 0, 0, 45)).toBe(45);
  });

  it("apiProgress 为 undefined 时 success 返回 100（无活跃 run）", () => {
    expect(deriveTaskProgress("success", 0, 0, undefined)).toBe(100);
  });

  it("apiProgress 为 undefined 时 warning 返回 100", () => {
    expect(deriveTaskProgress("warning", 0, 0, undefined)).toBe(100);
  });

  it("apiProgress 为 undefined 时 running 返回 0（不使用虚假值）", () => {
    expect(deriveTaskProgress("running", 0, 0, undefined)).toBe(0);
  });

  it("apiProgress 为 undefined 时 canceled/pending/skipped 返回 0", () => {
    expect(deriveTaskProgress("canceled", 0, 0, undefined)).toBe(0);
    expect(deriveTaskProgress("pending", 0, 0, undefined)).toBe(0);
    expect(deriveTaskProgress("skipped", 0, 0, undefined)).toBe(0);
  });

  it("apiProgress=100 覆盖任何 status（活跃 run 完成）", () => {
    expect(deriveTaskProgress("success", 0, 0, 100)).toBe(100);
    expect(deriveTaskProgress("running", 0, 0, 100)).toBe(100);
  });
});
