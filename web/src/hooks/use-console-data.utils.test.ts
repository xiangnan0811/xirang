import { describe, expect, it } from "vitest";
import { describeCron } from "./use-console-data.utils";

describe("describeCron", () => {
  it("正常 5 段 cron 走每日描述", () => {
    expect(describeCron("0 2 * * *")).toContain("02:00");
  });

  it("undefined 输入不抛异常，回退到原始表达式占位", () => {
    // 回归保护：mapPolicy 偶发收到不规范信封时 cron 可能为 undefined，
    // 旧实现会触发 cron.trim() 崩溃，导致整个策略页白屏。
    expect(() => describeCron(undefined)).not.toThrow();
    expect(describeCron(undefined)).toBeTypeOf("string");
  });

  it("空字符串与 null 同样安全降级", () => {
    expect(() => describeCron("")).not.toThrow();
    expect(() => describeCron(null)).not.toThrow();
  });
});
