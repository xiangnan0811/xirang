import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BrowserRouter } from "react-router-dom";
import { OnboardingTour } from "./onboarding-tour";

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/lib/api/core", () => ({
  request: vi.fn().mockResolvedValue({}),
}));

describe("OnboardingTour", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("首次访问时自动显示欢迎对话框", async () => {
    render(
      <BrowserRouter>
        <OnboardingTour />
      </BrowserRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/欢迎使用息壤/i)).toBeInTheDocument();
    });
  });

  it("点击开始引导后进入第一步", async () => {
    const user = userEvent.setup();
    render(
      <BrowserRouter>
        <OnboardingTour />
      </BrowserRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/欢迎使用息壤/i)).toBeInTheDocument();
    });

    const startButton = screen.getByRole("button", { name: /开始引导/i });
    await user.click(startButton);

    await waitFor(() => {
      expect(screen.getByText(/第 1 步/i)).toBeInTheDocument();
      // 在第一步应该同时看到返回欢迎页的“上一步”按钮
      expect(screen.getByRole("button", { name: /上一步/i })).toBeInTheDocument();
    });
  });

  it("支持纯顺序引导直到完成", async () => {
    const user = userEvent.setup();
    render(
      <BrowserRouter>
        <OnboardingTour />
      </BrowserRouter>
    );

    // 欢迎页 -> 第一步
    await waitFor(() => screen.getByRole("button", { name: /开始引导/i }));
    await user.click(screen.getByRole("button", { name: /开始引导/i }));
    await waitFor(() => screen.getByText(/第 1 步：配置 SSH Key/i));

    // 第一步 -> 第二步
    await user.click(screen.getByRole("button", { name: /下一步/i }));
    await waitFor(() => screen.getByText(/第 2 步：添加节点/i));

    // 第二步 -> 返回第一步
    await user.click(screen.getByRole("button", { name: /上一步/i }));
    await waitFor(() => screen.getByText(/第 1 步：配置 SSH Key/i));

    // 第一步 -> 第三步
    await user.click(screen.getByRole("button", { name: /下一步/i }));
    await waitFor(() => screen.getByText(/第 2 步：添加节点/i));
    await user.click(screen.getByRole("button", { name: /下一步/i }));
    await waitFor(() => screen.getByText(/第 3 步：创建策略/i));

    // 第三步 -> 第四步
    await user.click(screen.getByRole("button", { name: /下一步/i }));
    await waitFor(() => screen.getByText(/第 4 步：创建任务/i));

    // 第四步显示完成按钮
    const finishButton = screen.getByRole("button", { name: /完成/i });
    expect(finishButton).toBeInTheDocument();

    // 点击完成关闭引导
    await user.click(finishButton);
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("支持跳过引导", async () => {
    const user = userEvent.setup();
    render(
      <BrowserRouter>
        <OnboardingTour />
      </BrowserRouter>
    );

    // 欢迎页 -> 第一步
    await waitFor(() => screen.getByRole("button", { name: /开始引导/i }));
    await user.click(screen.getByRole("button", { name: /开始引导/i }));
    await waitFor(() => screen.getByText(/第 1 步：/i));

    // 点击跳过引导
    const skipButton = screen.getByRole("button", { name: /跳过引导/i });
    await user.click(skipButton);

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("支持关闭按钮跳过引导", async () => {
    const user = userEvent.setup();
    render(
      <BrowserRouter>
        <OnboardingTour />
      </BrowserRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
    });

    const closeButton = screen.getByRole("button", { name: /关闭/i });
    await user.click(closeButton);

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("支持点击步骤卡片跳转", async () => {
    const user = userEvent.setup();
    render(
      <BrowserRouter>
        <OnboardingTour />
      </BrowserRouter>
    );

    // 欢迎页 -> 第一步
    await waitFor(() => screen.getByRole("button", { name: /开始引导/i }));
    await user.click(screen.getByRole("button", { name: /开始引导/i }));
    await waitFor(() => screen.getByText(/第 1 步：配置 SSH Key/i));

    // 点击步骤3卡片直接跳转
    const step3Button = screen.getByRole("button", { name: /跳转到第 3 步：创建策略/i });
    await user.click(step3Button);

    await waitFor(() => {
      expect(screen.getByText(/第 3 步：创建策略/i)).toBeInTheDocument();
    });
  });

  it("支持键盘导航", async () => {
    const user = userEvent.setup();
    render(
      <BrowserRouter>
        <OnboardingTour />
      </BrowserRouter>
    );

    // 欢迎页 -> 第一步
    await waitFor(() => screen.getByRole("button", { name: /开始引导/i }));
    await user.click(screen.getByRole("button", { name: /开始引导/i }));
    await waitFor(() => screen.getByText(/第 1 步：配置 SSH Key/i));

    // 按右箭头前进到第二步
    await user.keyboard("{ArrowRight}");
    await waitFor(() => {
      expect(screen.getByText(/第 2 步：添加节点/i)).toBeInTheDocument();
    });

    // 按左箭头返回第一步
    await user.keyboard("{ArrowLeft}");
    await waitFor(() => {
      expect(screen.getByText(/第 1 步：配置 SSH Key/i)).toBeInTheDocument();
    });
  });

  it("支持不再显示选项", async () => {
    const user = userEvent.setup();
    render(
      <BrowserRouter>
        <OnboardingTour />
      </BrowserRouter>
    );

    // 欢迎页 -> 第一步
    await waitFor(() => screen.getByRole("button", { name: /开始引导/i }));
    await user.click(screen.getByRole("button", { name: /开始引导/i }));
    await waitFor(() => screen.getByText(/第 1 步：/i));

    // 点击不再显示
    const neverShowButton = screen.getByRole("button", { name: /不再显示/i });
    await user.click(neverShowButton);

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });
});
