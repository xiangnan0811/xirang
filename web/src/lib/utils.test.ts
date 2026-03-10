import { describe, expect, it } from "vitest";
import { getErrorMessage } from "./utils";

describe("getErrorMessage", () => {
  it("优先返回 Error.detail.error 中的后端错误信息", () => {
    const error = Object.assign(new Error("请求失败：400"), {
      detail: { error: "所选节点不存在，请重新选择" }
    });

    expect(getErrorMessage(error)).toBe("所选节点不存在，请重新选择");
  });

  it("detail 不可用时回退到 Error.message", () => {
    expect(getErrorMessage(new Error("操作失败，请稍候重试"))).toBe("操作失败，请稍候重试");
  });
});
