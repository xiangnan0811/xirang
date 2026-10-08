import { afterEach, describe, expect, it, beforeEach, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { AuthProvider } from "./auth-context";
import { useAuth } from "./auth-context.hooks";
import { saveStepUpProof } from "@/lib/step-up-storage";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { AuthTransitionRejectedError, clearAuthTransitionBarrier, dismissTOTPActivationFailure, request } from "@/lib/api/core";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    requestStepUpProof: vi.fn(),
  },
}));

const requestStepUpProofMock = vi.mocked(apiClient.requestStepUpProof);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function AuthProbe() {
  const { token, username, isAuthenticated, login, logout, setTotpEnabled, ensureStepUpProof } = useAuth();
  const [stepUpProof, setStepUpProof] = useState("pending");

  return (
    <div>
      <span data-testid="token">{token ?? "null"}</span>
      <span data-testid="username">{username ?? "null"}</span>
      <span data-testid="authenticated">{String(isAuthenticated)}</span>
      <span data-testid="step-up-proof">{stepUpProof}</span>
      <button type="button" onClick={() => login("auth-marker", "alice", "admin", 1, true)}>登录</button>
      <button type="button" onClick={() => logout()}>退出</button>
      <button type="button" onClick={() => setTotpEnabled(false)}>关闭两步验证</button>
      <button type="button" onClick={() => void ensureStepUpProof(STEP_UP_ACTIONS.taskManualTrigger).then(setStepUpProof)}>请求二次验证</button>
      <button type="button" onClick={() => void ensureStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, { persist: false, reuseCached: false }).then(setStepUpProof)}>请求一次性二次验证</button>
      <button type="button" onClick={() => void ensureStepUpProof(STEP_UP_ACTIONS.assetSecretReveal, { persist: true, reuseCached: false }).then(setStepUpProof)}>刷新密文验证</button>
    </div>
  );
}

function ConcurrentStepUpProbe() {
  const { login, ensureStepUpProof } = useAuth();
  const [results, setResults] = useState<string[]>([]);

  const requestTwice = () => {
    const record = (label: string) => (value: string) => {
      setResults((current) => [...current, `${label}:${value}`]);
    };
    const recordError = (label: string) => (error: unknown) => {
      setResults((current) => [
        ...current,
        `${label}:error:${error instanceof Error ? error.message : "unknown"}`,
      ]);
    };
    void ensureStepUpProof(STEP_UP_ACTIONS.assetSecretReveal).then(record("first"), recordError("first"));
    void ensureStepUpProof(STEP_UP_ACTIONS.assetSecretReveal).then(record("second"), recordError("second"));
  };

  return (
    <div>
      <button type="button" onClick={() => login("auth-marker", "alice", "admin", 1, true)}>登录并发测试</button>
      <button type="button" onClick={requestTwice}>并发请求二次验证</button>
      <span data-testid="concurrent-results">{results.join("|")}</span>
    </div>
  );
}

function StepUpBoundaryProbe() {
  const { login, logout, setTotpEnabled, ensureStepUpProof } = useAuth();
  const [result, setResult] = useState("pending");
  const request = () => {
    void ensureStepUpProof(STEP_UP_ACTIONS.assetSecretReveal).then(
      (proof) => setResult(proof),
      () => setResult("rejected"),
    );
  };

  return (
    <div>
      <button type="button" onClick={() => login("auth-marker", "alice", "admin", 1, true)}>登录边界测试</button>
      <button type="button" onClick={request}>请求边界验证</button>
      <button type="button" onClick={logout}>退出边界</button>
      <button type="button" onClick={() => login("replacement-token", "bob", "admin", 2, true)}>替换登录边界</button>
      <button type="button" onClick={() => setTotpEnabled(false)}>关闭验证边界</button>
      <button type="button" onClick={() => {
        sessionStorage.removeItem("xirang-auth-token");
        sessionStorage.removeItem("xirang-step-up-proofs-v2");
      }}>模拟 401 边界</button>
      <span data-testid="boundary-result">{result}</span>
    </div>
  );
}

function PrerequisiteProbe() {
  const { login, ensureStepUpProof } = useAuth();
  const [result, setResult] = useState("pending");
  const check = () => {
    void ensureStepUpProof(STEP_UP_ACTIONS.taskManualTrigger).then(
      () => setResult("proof"),
      (error: unknown) => setResult(error instanceof StepUpPrerequisiteError ? error.code : error instanceof Error ? error.message : "error"),
    );
  };

  return (
    <div>
      <button type="button" onClick={() => login("auth-marker", "alice", "admin", 1, false)}>登录未启用</button>
      <button type="button" onClick={check}>未登录检查</button>
      <button type="button" onClick={check}>未启用检查</button>
      <span data-testid="prerequisite">{result}</span>
    </div>
  );
}

function ActivationProbe() {
  const auth = useAuth();
  const [owner, setOwner] = useState<number | null>(null);
  const [completed, setCompleted] = useState("idle");

  return (
    <div>
      <span data-testid="token">{auth.token ?? "null"}</span>
      <span data-testid="user-id">{auth.userId ?? "null"}</span>
      <span data-testid="totp">{String(auth.totpEnabled)}</span>
      <span data-testid="transitioning">{String(auth.authTransitioning)}</span>
      <span data-testid="completed">{completed}</span>
      <button type="button" onClick={() => auth.login("old-token", "alice", "admin", 7, false)}>登录旧会话</button>
      <button type="button" onClick={() => setOwner(auth.beginTOTPActivation())}>开始启用</button>
      <button type="button" onClick={() => { if (owner !== null) auth.abortTOTPActivation(owner); }}>取消启用</button>
      <button type="button" onClick={() => {
        if (owner === null) return;
        const ok = auth.completeTOTPActivation(owner, "new-token", {
          id: 7,
          username: "alice",
          role: "admin",
          totpEnabled: true,
        });
        setCompleted(ok ? "yes" : "no");
      }}>完成启用</button>
      <button type="button" onClick={() => auth.logout()}>退出启用</button>
    </div>
  );
}

describe("AuthProvider", () => {
  beforeEach(() => {
    dismissTOTPActivationFailure();
    clearAuthTransitionBarrier();
    localStorage.clear();
    sessionStorage.clear();
    requestStepUpProofMock.mockReset();
  });

  afterEach(() => {
    dismissTOTPActivationFailure();
    clearAuthTransitionBarrier();
  });

  it("初始化时可迁移旧 localStorage 到 sessionStorage", () => {
    localStorage.setItem("xirang-auth-token", "persisted-token");
    localStorage.setItem("xirang-username", "persisted-user");

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    expect(screen.getByTestId("token").textContent).toBe("persisted-token");
    expect(screen.getByTestId("username").textContent).toBe("persisted-user");
    expect(screen.getByTestId("authenticated").textContent).toBe("true");
    expect(sessionStorage.getItem("xirang-auth-token")).toBe("persisted-token");
    expect(sessionStorage.getItem("xirang-username")).toBe("persisted-user");
    expect(localStorage.getItem("xirang-auth-token")).toBeNull();
    expect(localStorage.getItem("xirang-username")).toBeNull();
  });

  it("登录与退出会同步更新鉴权状态和 sessionStorage", async () => {
    const user = userEvent.setup();

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    expect(screen.getByTestId("authenticated").textContent).toBe("false");

    await user.click(screen.getByRole("button", { name: "登录" }));

    expect(screen.getByTestId("token").textContent).toBe("auth-marker");
    expect(screen.getByTestId("username").textContent).toBe("alice");
    expect(screen.getByTestId("authenticated").textContent).toBe("true");
    expect(sessionStorage.getItem("xirang-auth-token")).toBe("auth-marker");
    expect(sessionStorage.getItem("xirang-username")).toBe("alice");
    expect(localStorage.getItem("xirang-auth-token")).toBeNull();
    expect(localStorage.getItem("xirang-username")).toBeNull();

    await user.click(screen.getByRole("button", { name: "退出" }));

    expect(screen.getByTestId("token").textContent).toBe("null");
    expect(screen.getByTestId("username").textContent).toBe("null");
    expect(screen.getByTestId("authenticated").textContent).toBe("false");
    expect(sessionStorage.getItem("xirang-auth-token")).toBeNull();
    expect(sessionStorage.getItem("xirang-username")).toBeNull();
    expect(localStorage.getItem("xirang-auth-token")).toBeNull();
    expect(localStorage.getItem("xirang-username")).toBeNull();
  });

  it("登录、退出和关闭两步验证会清理 session 级 step-up proof", async () => {
    const user = userEvent.setup();
    saveStepUpProof(STEP_UP_ACTIONS.terminalOpen, "proof-before-login-marker", Date.now() + 60_000);

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    await user.click(screen.getByRole("button", { name: "登录" }));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();

    saveStepUpProof(STEP_UP_ACTIONS.configExport, "proof-before-disable-marker", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "关闭两步验证" }));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();

    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "proof-before-logout-marker", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "退出" }));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("ensureStepUpProof 会复用未过期 proof", async () => {
    const user = userEvent.setup();

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    await user.click(screen.getByRole("button", { name: "登录" }));
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "cached-step-up-marker", Date.now() + 60_000);

    await user.click(screen.getByRole("button", { name: "请求二次验证" }));

    expect(screen.getByTestId("step-up-proof").textContent).toBe("cached-step-up-marker");
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toContain("cached-step-up-marker");
    expect(screen.queryByText("需要二次验证")).toBeNull();
  });

  it("未登录或未启用两步验证时不会先读取缓存 proof", async () => {
    const user = userEvent.setup();
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "cached-step-up-marker", Date.now() + 60_000);
    render(
      <AuthProvider>
        <PrerequisiteProbe />
      </AuthProvider>
    );

    await user.click(screen.getByRole("button", { name: "未登录检查" }));
    expect(screen.getByTestId("prerequisite").textContent).toBe("请重新登录后继续。");
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toContain("cached-step-up-marker");
    expect(screen.queryByRole("dialog")).toBeNull();

    await user.click(screen.getByRole("button", { name: "登录未启用" }));
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "cached-step-up-marker", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "未启用检查" }));
    expect(screen.getByTestId("prerequisite").textContent).toBe("TOTP_REQUIRED");
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toContain("cached-step-up-marker");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("可请求一次性 step-up proof 而不复用且清除 sessionStorage proof", async () => {
    const user = userEvent.setup();
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "cached-step-up-marker", Date.now() + 60_000);
    requestStepUpProofMock.mockResolvedValueOnce({
      proof: "one-time-step-up-marker",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      proofTtlSeconds: 60,
    });

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    await user.click(screen.getByRole("button", { name: "登录" }));
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "cached-step-up-marker", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "请求一次性二次验证" }));
    expect(await screen.findByRole("dialog", { name: "需要二次验证" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("验证器验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));

    await waitFor(() => expect(screen.getByTestId("step-up-proof").textContent).toBe("one-time-step-up-marker"));
    expect(requestStepUpProofMock).toHaveBeenCalledWith("auth-marker", "123456", STEP_UP_ACTIONS.taskManualTrigger);
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("强制刷新可持久 proof 时会先清除被拒绝的缓存", async () => {
    const user = userEvent.setup();
    requestStepUpProofMock.mockResolvedValueOnce({
      proof: "fresh-secret-proof-marker",
      expiresAt: new Date(Date.now() + 45 * 60_000).toISOString(),
      proofTtlSeconds: 45 * 60,
    });
    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );
    await user.click(screen.getByRole("button", { name: "登录" }));
    saveStepUpProof(
      STEP_UP_ACTIONS.assetSecretReveal,
      "rejected-secret-proof-marker",
      Date.now() + 45 * 60_000,
    );

    await user.click(screen.getByRole("button", { name: "刷新密文验证" }));
    expect(await screen.findByRole("dialog", { name: "需要二次验证" })).toBeInTheDocument();
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2") ?? "").not.toContain("rejected-secret-proof-marker");

    await user.type(screen.getByLabelText("验证器验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));
    await waitFor(() => expect(screen.getByTestId("step-up-proof").textContent).toBe("fresh-secret-proof-marker"));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toContain("fresh-secret-proof-marker");
  });

  it("不会用客户端接收时间为无效的服务端 expiry 派生滑动缓存", async () => {
    const user = userEvent.setup();
    requestStepUpProofMock.mockResolvedValueOnce({
      proof: "invalid-expiry-proof-marker",
      expiresAt: "not-an-expiry",
      proofTtlSeconds: 45 * 60,
    });
    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );
    await user.click(screen.getByRole("button", { name: "登录" }));
    await user.click(screen.getByRole("button", { name: "刷新密文验证" }));
    await user.type(screen.getByLabelText("验证器验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));

    await waitFor(() => expect(screen.getByTestId("step-up-proof").textContent).toBe("invalid-expiry-proof-marker"));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("成功签发后下一次验证码输入仍可提交", async () => {
    const user = userEvent.setup();
    const freshProof = (proof: string) => ({
      proof,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      proofTtlSeconds: 60,
    });
    requestStepUpProofMock
      .mockResolvedValueOnce(freshProof("first-fresh-proof"))
      .mockResolvedValueOnce(freshProof("second-fresh-proof"));

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );
    await user.click(screen.getByRole("button", { name: "登录" }));

    await user.click(screen.getByRole("button", { name: "请求一次性二次验证" }));
    const firstCode = await screen.findByLabelText("验证器验证码");
    expect(firstCode).toBeEnabled();
    await user.type(firstCode, "111111");
    await user.click(screen.getByRole("button", { name: "验证" }));
    await waitFor(() => expect(screen.getByTestId("step-up-proof").textContent).toBe("first-fresh-proof"));
    expect(screen.queryByRole("dialog", { name: "需要二次验证" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "请求一次性二次验证" }));
    const secondCode = await screen.findByLabelText("验证器验证码");
    expect(secondCode).toBeEnabled();
    await user.type(secondCode, "222222");
    await user.click(screen.getByRole("button", { name: "验证" }));
    await waitFor(() => expect(screen.getByTestId("step-up-proof").textContent).toBe("second-fresh-proof"));
    expect(requestStepUpProofMock).toHaveBeenNthCalledWith(1, "auth-marker", "111111", STEP_UP_ACTIONS.taskManualTrigger);
    expect(requestStepUpProofMock).toHaveBeenNthCalledWith(2, "auth-marker", "222222", STEP_UP_ACTIONS.taskManualTrigger);
  });

  it("同 action 并发请求共用一个弹窗和同一签发结果", async () => {
    const user = userEvent.setup();
    requestStepUpProofMock.mockResolvedValueOnce({
      proof: "shared-secret-proof-marker",
      expiresAt: new Date(Date.now() + 45 * 60_000).toISOString(),
      proofTtlSeconds: 45 * 60,
    });
    render(
      <AuthProvider>
        <ConcurrentStepUpProbe />
      </AuthProvider>
    );
    await user.click(screen.getByRole("button", { name: "登录并发测试" }));

    await user.click(screen.getByRole("button", { name: "并发请求二次验证" }));
    expect(await screen.findByRole("dialog", { name: "需要二次验证" })).toBeInTheDocument();
    expect(screen.getAllByRole("dialog", { name: "需要二次验证" })).toHaveLength(1);
    await user.type(screen.getByLabelText("验证器验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));

    await waitFor(() => expect(screen.getByTestId("concurrent-results").textContent).toContain(
      "first:shared-secret-proof-marker"
    ));
    expect(screen.getByTestId("concurrent-results").textContent).toContain(
      "second:shared-secret-proof-marker"
    );
    expect(screen.getByTestId("concurrent-results").textContent).not.toContain("error:");
    expect(requestStepUpProofMock).toHaveBeenCalledTimes(1);
  });

  it.each(["退出边界", "替换登录边界", "关闭验证边界", "模拟 401 边界"])(
    "%s 会阻止在途签发结果重新持久化 proof",
    async (boundaryButton) => {
      const user = userEvent.setup();
      const pending = deferred<{
        proof: string;
        expiresAt: string;
        proofTtlSeconds: number;
      }>();
      requestStepUpProofMock.mockReturnValueOnce(pending.promise);
      render(
        <AuthProvider>
          <StepUpBoundaryProbe />
        </AuthProvider>
      );
      await user.click(screen.getByRole("button", { name: "登录边界测试" }));
      await user.click(screen.getByRole("button", { name: "请求边界验证" }));
      await user.type(screen.getByLabelText("验证器验证码"), "123456");
      await user.click(screen.getByRole("button", { name: "验证" }));

      await act(async () => {
        screen.getByText(boundaryButton).click();
      });
      await act(async () => {
        pending.resolve({
          proof: "stale-boundary-proof",
          expiresAt: new Date(Date.now() + 45 * 60_000).toISOString(),
          proofTtlSeconds: 45 * 60,
        });
        await pending.promise;
      });

      await waitFor(() => expect(screen.getByTestId("boundary-result").textContent).toBe("rejected"));
      expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
      expect(screen.queryByRole("dialog", { name: "需要二次验证" })).toBeNull();
    }
  );
});

describe("TOTP activation session", () => {
  beforeEach(() => {
    dismissTOTPActivationFailure();
    clearAuthTransitionBarrier();
    localStorage.clear();
    sessionStorage.clear();
  });

  afterEach(() => {
    dismissTOTPActivationFailure();
    clearAuthTransitionBarrier();
    vi.restoreAllMocks();
  });

  it("开始启用会占住 owner、清掉 proof，并在请求发出前拒绝其他请求", async () => {
    const user = userEvent.setup();
    render(<AuthProvider><ActivationProbe /></AuthProvider>);
    await user.click(screen.getByRole("button", { name: "登录旧会话" }));
    saveStepUpProof(STEP_UP_ACTIONS.terminalOpen, "proof-before-activation", Date.now() + 60_000);

    await user.click(screen.getByRole("button", { name: "开始启用" }));

    expect(screen.getByTestId("transitioning").textContent).toBe("true");
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
    expect(sessionStorage.getItem("xirang-auth-token")).toBe("old-token");
    await expect(request("/nodes", { token: "old-token" })).rejects.toBeInstanceOf(AuthTransitionRejectedError);
  });

  it("取消启用释放阻断并保留旧会话", async () => {
    const user = userEvent.setup();
    render(<AuthProvider><ActivationProbe /></AuthProvider>);
    await user.click(screen.getByRole("button", { name: "登录旧会话" }));
    await user.click(screen.getByRole("button", { name: "开始启用" }));
    await user.click(screen.getByRole("button", { name: "取消启用" }));

    expect(screen.getByTestId("transitioning").textContent).toBe("false");
    expect(screen.getByTestId("token").textContent).toBe("old-token");
    expect(screen.getByTestId("totp").textContent).toBe("false");
  });

  it("完成启用安装替换会话，退出后迟到完成不能复活", async () => {
    const user = userEvent.setup();
    render(<AuthProvider><ActivationProbe /></AuthProvider>);
    await user.click(screen.getByRole("button", { name: "登录旧会话" }));
    await user.click(screen.getByRole("button", { name: "开始启用" }));
    await user.click(screen.getByRole("button", { name: "完成启用" }));

    expect(screen.getByTestId("completed").textContent).toBe("yes");
    expect(screen.getByTestId("token").textContent).toBe("new-token");
    expect(screen.getByTestId("user-id").textContent).toBe("7");
    expect(screen.getByTestId("totp").textContent).toBe("true");
    expect(screen.getByTestId("transitioning").textContent).toBe("false");
    expect(sessionStorage.getItem("xirang-auth-token")).toBe("new-token");
    expect(sessionStorage.getItem("xirang-totp-enabled")).toBe("true");

    await user.click(screen.getByRole("button", { name: "退出启用" }));
    await user.click(screen.getByRole("button", { name: "完成启用" }));
    expect(screen.getByTestId("completed").textContent).toBe("no");
    expect(screen.getByTestId("token").textContent).toBe("null");
    expect(sessionStorage.getItem("xirang-auth-token")).toBeNull();
  });

  it("会话安装失败时清理登录并保留失败提示", async () => {
    const user = userEvent.setup();
    const original = Storage.prototype.setItem;
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function setItem(this: Storage, key: string, value: string) {
      if (value === "new-token") {
        throw new Error("quota");
      }
      return original.call(this, key, value);
    });
    try {
      render(<AuthProvider><ActivationProbe /></AuthProvider>);
      await user.click(screen.getByRole("button", { name: "登录旧会话" }));
      await user.click(screen.getByRole("button", { name: "开始启用" }));
      await user.click(screen.getByRole("button", { name: "完成启用" }));

      expect(screen.getByTestId("completed").textContent).toBe("no");
      expect(screen.getByTestId("token").textContent).toBe("null");
      expect(sessionStorage.getItem("xirang-auth-token")).toBeNull();
    } finally {
      spy.mockRestore();
    }
  });
});
