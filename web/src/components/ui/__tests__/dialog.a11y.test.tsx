import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { axe } from "vitest-axe";

import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "../dialog";

describe("Dialog a11y", () => {
  // Wave 4 PR-A: vitest-axe smoke 测试 — 确保 a11y 流水线绿色（在线性 jsdom 环境）。
  // Radix Dialog 通过 portal 渲染到 document.body，因此用整个 body 作为 axe 扫描根。
  it("smoke: 默认渲染无 axe 违规", async () => {
    render(
      <Dialog open onOpenChange={() => {}}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>对话框标题</DialogTitle>
            <DialogDescription>用于无障碍冒烟测试的描述文案。</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <p>对话框正文内容，提供基本可读结构。</p>
          </DialogBody>
        </DialogContent>
      </Dialog>,
    );

    const results = await axe(document.body);
    expect(results).toHaveNoViolations();
  });
});
