import { describe, expect, it } from "vitest";
import { headersToJSON, parseHeaders } from "./service-monitor-headers";

describe("service-monitor-headers", () => {
  it("parseHeaders 将后端 JSON 字符串解析为 key/value 数组", () => {
    expect(parseHeaders('{"X-Token":"abc","Accept":"application/json"}')).toEqual([
      { key: "X-Token", value: "abc" },
      { key: "Accept", value: "application/json" },
    ]);
  });

  it("parseHeaders 对非对象 JSON 退化为空数组", () => {
    expect(parseHeaders("[]")).toEqual([]);
    expect(parseHeaders("not-json")).toEqual([]);
    expect(parseHeaders("{}")).toEqual([]);
    expect(parseHeaders(undefined)).toEqual([]);
  });

  it("headersToJSON 将 key/value 数组还原为后端 JSON 字符串，并跳过空 key", () => {
    expect(
      headersToJSON([
        { key: "X-Token", value: "abc" },
        { key: "  ", value: "ignored" },
      ])
    ).toBe('{"X-Token":"abc"}');
  });

  it("headersToJSON 对空数组返回 '{}'", () => {
    expect(headersToJSON([])).toBe("{}");
  });

  it("parseHeaders 与 headersToJSON 可往返（忽略空 key）", () => {
    const raw = '{"A":"1","B":"2"}';
    expect(headersToJSON(parseHeaders(raw))).toBe(raw);
  });
});
