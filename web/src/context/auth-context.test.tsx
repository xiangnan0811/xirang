import { afterEach, describe, expect, it, beforeEach, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useEffect, useRef, useState } from "react";
import i18n from "@/i18n";
import { AuthProvider } from "./auth-context";
import { useAuth } from "./auth-context.hooks";
import { saveStepUpProof } from "@/lib/step-up-storage";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS, type StepUpProofResponse } from "@/lib/api/totp-api";
import { AuthTransitionRejectedError, bumpAuthSessionGeneration, clearAuthTransitionBarrier, dismissTOTPActivationFailure, request } from "@/lib/api/core";
import type { CronBackupStatus } from "@/lib/api/cron-backup-status";
import type { BackupEntry } from "@/lib/api/system-api";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";
import { SelfBackupPanel } from "@/components/self-backup-panel";

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    requestStepUpProof: vi.fn(),
    listBackups: vi.fn(),
    getCronBackupStatus: vi.fn(),
    requestSystemBackupDownloadCredentialGrant: vi.fn(),
    downloadBackup: vi.fn(),
  },
}));

const requestStepUpProofMock = vi.mocked(apiClient.requestStepUpProof);
const listBackupsMock = vi.mocked(apiClient.listBackups);
const getCronBackupStatusMock = vi.mocked(apiClient.getCronBackupStatus);
const requestDownloadGrantMock = vi.mocked(apiClient.requestSystemBackupDownloadCredentialGrant);
const downloadBackupMock = vi.mocked(apiClient.downloadBackup);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function proofResult(proof: string): StepUpProofResponse {
  return { proof, expiresAt: new Date(Date.now() + 60_000).toISOString(), proofTtlSeconds: 60 };
}

function submittedSignal(callIndex: number): AbortSignal | undefined {
  return requestStepUpProofMock.mock.calls[callIndex]?.[3]?.signal;
}

// Buttons outside an open Radix dialog are aria-hidden/pointer-blocked; click them directly like the boundary tests.
async function clickRaw(label: string) {
  await act(async () => {
    screen.getByText(label).click();
  });
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

function OwnedStepUpProbe({ record, persist = false }: { record: (entry: string) => void; persist?: boolean }) {
  const { login, logout, ensureStepUpProof } = useAuth();
  const controllersRef = useRef<AbortController[]>([]);

  useEffect(() => {
    const controllers = controllersRef.current;
    return () => {
      for (const controller of controllers) {
        controller.abort();
      }
    };
  }, []);

  const request = (label: string, signal: AbortSignal | undefined) => {
    void ensureStepUpProof(STEP_UP_ACTIONS.systemBackupDownload, { persist, reuseCached: false, signal }).then(
      (proof) => record(`${label}:${proof}`),
      (error: unknown) => record(`${label}:${
        error instanceof DOMException && error.name === "AbortError" ? "AbortError" : error instanceof Error ? error.message : "unknown"
      }`),
    );
  };
  const requestOwned = (label: string) => {
    const controller = new AbortController();
    controllersRef.current.push(controller);
    request(label, controller.signal);
  };

  return (
    <div>
      <button type="button" onClick={() => login("auth-marker", "alice", "admin", 1, true)}>登录所有权测试</button>
      <button type="button" onClick={() => requestOwned("a")}>所有者A请求</button>
      <button type="button" onClick={() => requestOwned("b")}>所有者B请求</button>
      <button type="button" onClick={() => request("shared", controllersRef.current.at(-1)?.signal)}>同所有者再次请求</button>
      <button type="button" onClick={() => request("unowned", undefined)}>无所有者请求</button>
      <button type="button" onClick={() => {
        const controller = new AbortController();
        controller.abort();
        request("pre", controller.signal);
      }}>已中止所有者请求</button>
      <button type="button" onClick={() => controllersRef.current[0]?.abort()}>中止所有者A</button>
      <button type="button" onClick={logout}>所有权退出</button>
    </div>
  );
}

function OwnedStepUpHost({ record }: { record: (entry: string) => void }) {
  const [mounted, setMounted] = useState(true);
  return (
    <div>
      {mounted ? <OwnedStepUpProbe record={record} /> : null}
      <button type="button" onClick={() => setMounted(false)}>卸载所有者</button>
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

describe("AuthProvider owned step-up attempts", () => {
  const dialogName = "需要二次验证";

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

  async function submitCode(user: UserEvent, code: string) {
    await user.type(await screen.findByLabelText("验证器验证码"), code);
    await user.click(screen.getByRole("button", { name: "验证" }));
  }

  it("已中止的所有者不会打开验证框也不会发起签发", async () => {
    const log: string[] = [];
    render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => log.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("已中止所有者请求");

    await waitFor(() => expect(log).toEqual(["pre:AbortError"]));
    expect(screen.queryByRole("dialog", { name: dialogName })).toBeNull();
    expect(requestStepUpProofMock).not.toHaveBeenCalled();
  });

  it("等待验证码时所有者卸载会关闭验证框并以 AbortError 拒绝", async () => {
    const log: string[] = [];
    render(
      <AuthProvider>
        <OwnedStepUpHost record={(entry) => log.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("所有者A请求");
    const code = await screen.findByLabelText("验证器验证码");
    await userEvent.setup().type(code, "123");

    await clickRaw("卸载所有者");

    await waitFor(() => expect(log).toEqual(["a:AbortError"]));
    expect(screen.queryByRole("dialog", { name: dialogName })).toBeNull();
    expect(requestStepUpProofMock).not.toHaveBeenCalled();
  });

  it("提交在途时所有者中止会取消 POST，新的同 action 所有者不受旧结果和旧 finally 影响", async () => {
    const user = userEvent.setup();
    const log: string[] = [];
    const stale = deferred<StepUpProofResponse>();
    const fresh = deferred<StepUpProofResponse>();
    requestStepUpProofMock.mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);
    render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => log.push(entry)} persist />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("所有者A请求");
    await submitCode(user, "111111");
    await waitFor(() => expect(requestStepUpProofMock).toHaveBeenCalledTimes(1));
    expect(submittedSignal(0)?.aborted).toBe(false);

    await clickRaw("中止所有者A");

    await waitFor(() => expect(log).toEqual(["a:AbortError"]));
    expect(submittedSignal(0)?.aborted).toBe(true);
    expect(screen.queryByRole("dialog", { name: dialogName })).toBeNull();

    await clickRaw("所有者B请求");
    const code = await screen.findByLabelText("验证器验证码");
    expect(code).toBeEnabled();
    expect(code).toHaveValue("");
    await submitCode(user, "222222");
    await waitFor(() => expect(requestStepUpProofMock).toHaveBeenCalledTimes(2));
    expect(requestStepUpProofMock).toHaveBeenNthCalledWith(
      2,
      "auth-marker",
      "222222",
      STEP_UP_ACTIONS.systemBackupDownload,
      { signal: expect.any(AbortSignal) },
    );
    expect(submittedSignal(1)).not.toBe(submittedSignal(0));
    expect(screen.getByLabelText("验证器验证码")).toBeDisabled();

    await act(async () => {
      stale.resolve(proofResult("stale-owner-a-proof"));
      await stale.promise;
    });

    expect(log).toEqual(["a:AbortError"]);
    expect(screen.getByRole("dialog", { name: dialogName })).toBeInTheDocument();
    expect(screen.getByLabelText("验证器验证码")).toBeDisabled();
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();

    await act(async () => {
      fresh.resolve(proofResult("fresh-owner-b-proof"));
      await fresh.promise;
    });

    await waitFor(() => expect(log).toEqual(["a:AbortError", "b:fresh-owner-b-proof"]));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2") ?? "").not.toContain("stale-owner-a-proof");
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toContain("fresh-owner-b-proof");
    expect(screen.queryByRole("dialog", { name: dialogName })).toBeNull();
    expect(requestStepUpProofMock).toHaveBeenCalledTimes(2);
  });

  it("所有者提交中仍可通过取消按钮或 Escape 撤销，且不会自动重放", async () => {
    const user = userEvent.setup();
    const log: string[] = [];
    const first = deferred<StepUpProofResponse>();
    const second = deferred<StepUpProofResponse>();
    requestStepUpProofMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => log.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("所有者A请求");
    await submitCode(user, "111111");
    const cancel = screen.getByRole("button", { name: i18n.t("common.cancel") });
    expect(cancel).toBeEnabled();
    await user.click(cancel);

    await waitFor(() => expect(log).toEqual([`a:${i18n.t("stepUp.cancelled")}`]));
    expect(submittedSignal(0)?.aborted).toBe(true);
    expect(screen.queryByRole("dialog", { name: dialogName })).toBeNull();

    await clickRaw("所有者B请求");
    await submitCode(user, "222222");
    await waitFor(() => expect(requestStepUpProofMock).toHaveBeenCalledTimes(2));
    await user.keyboard("{Escape}");

    await waitFor(() => expect(log).toEqual([`a:${i18n.t("stepUp.cancelled")}`, `b:${i18n.t("stepUp.cancelled")}`]));
    expect(submittedSignal(1)?.aborted).toBe(true);
    expect(screen.queryByRole("dialog", { name: dialogName })).toBeNull();

    await act(async () => {
      first.resolve(proofResult("late-first-proof"));
      second.resolve(proofResult("late-second-proof"));
      await Promise.all([first.promise, second.promise]);
    });
    expect(log).toHaveLength(2);
    expect(requestStepUpProofMock).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("dialog", { name: dialogName })).toBeNull();
  });

  it("无所有者提交中保持不可取消，并继续使用原有签名", async () => {
    const user = userEvent.setup();
    const log: string[] = [];
    const pending = deferred<StepUpProofResponse>();
    requestStepUpProofMock.mockReturnValueOnce(pending.promise);
    render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => log.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("无所有者请求");
    await submitCode(user, "123456");

    expect(screen.getByRole("button", { name: i18n.t("common.cancel") })).toBeDisabled();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: dialogName })).toBeInTheDocument();
    expect(log).toEqual([]);

    await act(async () => {
      pending.resolve(proofResult("unowned-proof"));
      await pending.promise;
    });

    await waitFor(() => expect(log).toEqual(["unowned:unowned-proof"]));
    expect(requestStepUpProofMock).toHaveBeenCalledWith("auth-marker", "123456", STEP_UP_ACTIONS.systemBackupDownload);
    expect(requestStepUpProofMock.mock.calls[0]).toHaveLength(3);
  });

  it("仅同一 signal/action/persist 共享待验证请求，其他调用沿用 alreadyOpen 冲突", async () => {
    const user = userEvent.setup();
    const log: string[] = [];
    requestStepUpProofMock.mockResolvedValueOnce(proofResult("owner-a-proof"));
    render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => log.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("所有者A请求");
    await screen.findByRole("dialog", { name: dialogName });
    await clickRaw("同所有者再次请求");
    await clickRaw("所有者B请求");
    await clickRaw("无所有者请求");

    await waitFor(() => expect(log).toEqual([
      `b:${i18n.t("stepUp.alreadyOpen")}`,
      `unowned:${i18n.t("stepUp.alreadyOpen")}`,
    ]));
    expect(screen.getAllByRole("dialog", { name: dialogName })).toHaveLength(1);

    await submitCode(user, "123456");
    await waitFor(() => expect(log).toContain("a:owner-a-proof"));
    expect(log).toContain("shared:owner-a-proof");
    expect(requestStepUpProofMock).toHaveBeenCalledTimes(1);
  });

  it("无所有者待验证时，带所有者的调用不复用它", async () => {
    const log: string[] = [];
    render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => log.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("无所有者请求");
    await screen.findByRole("dialog", { name: dialogName });
    await clickRaw("所有者A请求");

    await waitFor(() => expect(log).toEqual([`a:${i18n.t("stepUp.alreadyOpen")}`]));
    expect(screen.getByRole("dialog", { name: dialogName })).toBeInTheDocument();
  });

  it("旧所有者成功后再中止不会关闭新所有者的验证框", async () => {
    const user = userEvent.setup();
    const log: string[] = [];
    requestStepUpProofMock.mockResolvedValueOnce(proofResult("owner-a-proof"));
    render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => log.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("所有者A请求");
    await submitCode(user, "123456");
    await waitFor(() => expect(log).toEqual(["a:owner-a-proof"]));

    await clickRaw("所有者B请求");
    await screen.findByRole("dialog", { name: dialogName });
    await clickRaw("中止所有者A");

    expect(screen.getByRole("dialog", { name: dialogName })).toBeInTheDocument();
    expect(log).toEqual(["a:owner-a-proof"]);
  });

  it("退出会中止所有者在途 POST 并拒绝待验证请求", async () => {
    const user = userEvent.setup();
    const log: string[] = [];
    const pending = deferred<StepUpProofResponse>();
    requestStepUpProofMock.mockReturnValueOnce(pending.promise);
    render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => log.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("所有者A请求");
    await submitCode(user, "123456");
    await waitFor(() => expect(requestStepUpProofMock).toHaveBeenCalledTimes(1));

    await clickRaw("所有权退出");

    await waitFor(() => expect(log).toEqual([`a:${i18n.t("stepUp.loginRequired")}`]));
    expect(submittedSignal(0)?.aborted).toBe(true);
    await act(async () => {
      pending.resolve(proofResult("late-after-logout"));
      await pending.promise;
    });
    expect(log).toHaveLength(1);
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("Provider 卸载会中止在途 POST 并拒绝所有待验证请求", async () => {
    const user = userEvent.setup();
    const ownedLog: string[] = [];
    const pending = deferred<StepUpProofResponse>();
    requestStepUpProofMock.mockReturnValueOnce(pending.promise);
    const owned = render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => ownedLog.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("登录所有权测试");
    await clickRaw("所有者A请求");
    await submitCode(user, "123456");
    await waitFor(() => expect(requestStepUpProofMock).toHaveBeenCalledTimes(1));
    owned.unmount();
    await waitFor(() => expect(ownedLog).toEqual(["a:AbortError"]));
    expect(submittedSignal(0)?.aborted).toBe(true);

    const unownedLog: string[] = [];
    const unowned = render(
      <AuthProvider>
        <OwnedStepUpProbe record={(entry) => unownedLog.push(entry)} />
      </AuthProvider>
    );
    await clickRaw("无所有者请求");
    await screen.findByRole("dialog", { name: dialogName });
    unowned.unmount();
    await waitFor(() => expect(unownedLog).toEqual(["unowned:AbortError"]));
  });
});

describe("AuthProvider owned step-up with the actual system backup panel", () => {
  const backupFile = "xirang-20261018-010203.db";
  const backupEntry: BackupEntry = {
    source: "web",
    engine: "sqlite",
    filename: backupFile,
    size: 4,
    createdAt: "2026-10-18T01:02:03Z",
    sha256: "a".repeat(64),
    downloadable: true,
    unavailableReason: "",
    packagedSize: 3072,
  };
  const cronNotConfigured: CronBackupStatus = {
    status: "not_configured",
    engine: "sqlite",
    checkedAt: "2026-10-18T00:00:00Z",
    maxAgeSeconds: 93600,
    evidence: "artifact_pair",
    timeSource: "mtime",
    contentVerified: false,
    job: { evidence: "job_record", status: "not_configured", checkedAt: "2026-10-18T00:00:00Z", maxAgeSeconds: 93600 },
  };

  function PanelHost() {
    const { login } = useAuth();
    return (
      <div>
        <button type="button" onClick={() => login("auth-marker", "alice", "admin", 1, true)}>登录备份面板</button>
        <SelfBackupPanel />
      </div>
    );
  }

  beforeEach(() => {
    dismissTOTPActivationFailure();
    clearAuthTransitionBarrier();
    localStorage.clear();
    sessionStorage.clear();
    requestStepUpProofMock.mockReset();
    listBackupsMock.mockReset();
    getCronBackupStatusMock.mockReset();
    requestDownloadGrantMock.mockReset();
    downloadBackupMock.mockReset();
    listBackupsMock.mockImplementation(async (_token, options) => (options?.source === "cron" ? [] : [backupEntry]));
    getCronBackupStatusMock.mockResolvedValue(cronNotConfigured);
  });

  afterEach(() => {
    dismissTOTPActivationFailure();
    clearAuthTransitionBarrier();
  });

  it("仅会话代次变化即撤销面板持有的验证：关闭验证框、中止 POST，迟到证明不再签发授权或下载", async () => {
    const user = userEvent.setup();
    const pendingProof = deferred<StepUpProofResponse>();
    requestStepUpProofMock.mockReturnValueOnce(pendingProof.promise);
    render(
      <AuthProvider>
        <PanelHost />
      </AuthProvider>
    );
    await clickRaw("登录备份面板");

    const rowAction = i18n.t("selfBackup.download.rowAction", { filename: backupFile });
    fireEvent.click(await screen.findByRole("button", { name: rowAction }));
    await screen.findByRole("dialog", { name: i18n.t("selfBackup.download.title") });
    fireEvent.change(screen.getByLabelText(i18n.t("selfBackup.download.reasonLabel")), { target: { value: "offsite copy" } });
    fireEvent.click(screen.getByRole("button", { name: i18n.t("selfBackup.download.confirm") }));

    // The actual Provider prompt is owned by the panel's download signal.
    await screen.findByRole("dialog", { name: "需要二次验证" });
    await user.type(screen.getByLabelText("验证器验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));
    await waitFor(() => expect(requestStepUpProofMock).toHaveBeenCalledTimes(1));
    expect(requestStepUpProofMock).toHaveBeenCalledWith(
      "auth-marker",
      "123456",
      STEP_UP_ACTIONS.systemBackupDownload,
      { signal: expect.any(AbortSignal) },
    );
    const postSignal = submittedSignal(0);
    expect(postSignal?.aborted).toBe(false);
    const listCallsBeforeBump = listBackupsMock.mock.calls.length;

    // Token, role and storage stay identical: only the session generation moves.
    act(() => {
      bumpAuthSessionGeneration();
    });

    expect(postSignal?.aborted).toBe(true);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "需要二次验证" })).toBeNull());
    expect(screen.queryByRole("dialog", { name: i18n.t("selfBackup.download.title") })).toBeNull();
    // The new session content reloads read-only data and offers a fresh, idle download action.
    await waitFor(() => expect(listBackupsMock.mock.calls.length).toBeGreaterThan(listCallsBeforeBump));
    expect(await screen.findByRole("button", { name: rowAction })).toBeEnabled();

    await act(async () => {
      pendingProof.resolve(proofResult("late-generation-proof"));
      await pendingProof.promise;
    });

    expect(requestDownloadGrantMock).not.toHaveBeenCalled();
    expect(downloadBackupMock).not.toHaveBeenCalled();
    expect(screen.queryByText(i18n.t("selfBackup.download.doneCompat"))).toBeNull();
    expect(screen.queryByText(i18n.t("selfBackup.download.doneNative"))).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(requestStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2") ?? "").not.toContain("late-generation-proof");
  });
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
