import { describe, expect, it } from "vitest";
import { escapeCSVValue } from "@/pages/nodes-page.utils";

describe("escapeCSVValue", () => {
  it("普通值原样返回", () => {
    expect(escapeCSVValue("hello")).toBe("hello");
  });

  it("含逗号时加双引号包裹", () => {
    expect(escapeCSVValue("a,b")).toBe('"a,b"');
  });

  it("含双引号时转义并包裹", () => {
    expect(escapeCSVValue('say "hi"')).toBe('"say ""hi"""');
  });

  it("含换行时加双引号包裹", () => {
    expect(escapeCSVValue("line1\nline2")).toBe('"line1\nline2"');
  });

  it.each([
    ["=SUM(A1)", "\"'=SUM(A1)\""],
    ["+cmd", "\"'+cmd\""],
    ["-cmd", "\"'-cmd\""],
    ["@import", "\"'@import\""],
    ["\tcmd", "\"'\tcmd\""],
    ["\rcmd", "\"'\rcmd\""],
  ])("直接以公式前缀开头 %s → 前置单引号并包裹", (input, expected) => {
    expect(escapeCSVValue(input)).toBe(expected);
  });

  it.each([
    [" =HYPERLINK()", "\"' =HYPERLINK()\""],
    ["  +cmd", "\"'  +cmd\""],
    [" \t-payload", "\"' \t-payload\""],
    ["   @import", "\"'   @import\""],
  ])("前导空白后接公式前缀 %s → 仍防护", (input, expected) => {
    expect(escapeCSVValue(input)).toBe(expected);
  });

  it("前导空白后接普通字符不做前缀处理", () => {
    // 含空格会触发 safe !== value 为 false，且无逗号/引号/换行 → 原样返回
    expect(escapeCSVValue(" hello")).toBe(" hello");
  });
});
