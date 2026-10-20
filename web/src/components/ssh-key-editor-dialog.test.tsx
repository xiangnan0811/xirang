import type { ComponentProps } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, bumpAuthSessionGeneration } from "@/lib/api/core";
import { SSHKeyUpdateDecodeError } from "@/lib/api/ssh-keys-api";
import { runSSHKeyReplacement, type SSHKeyReplacementOptions } from "@/lib/ssh-key-replacement";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";
import type { SSHKeyRecord } from "@/types/domain";
import i18n from "@/i18n";
import { SSHKeyEditorDialog, type SSHKeyDraft } from "./ssh-key-editor-dialog";

type ProofOptions = { persist?: boolean; reuseCached?: boolean; signal?: AbortSignal };

const { toastError, toastSuccess, ensureStepUpProof, authRef } = vi.hoisted(() => {
  const ensureStepUpProof = vi.fn<(action: string, options?: ProofOptions) => Promise<string>>();
  const authRef = {
    current: {
      role: "admin" as "admin" | "operator" | "viewer" | null,
      token: "token-1" as string | null,
      totpEnabled: true,
      authTransitioning: false,
      ensureStepUpProof,
    },
  };
  return {
    toastError: vi.fn(),
    toastSuccess: vi.fn(),
    ensureStepUpProof,
    authRef,
  };
});

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}));

const UNKNOWN_TEXT = "无法确认是否已保存。请到密钥页核对当前公钥指纹，不要重试本次提交。";
const INCOMPLETE_TEXT = "二次验证未完成，未继续保存";
const EXPIRED_TEXT = "二次验证已失效，请重新确认";

function createStepUpRequiredError() {
  const error = new ApiError(403, "需要二次验证", {
    code: 403,
    message: "需要二次验证",
    data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
  });
  error.httpStatus = 403;
  return error;
}

function httpError(status: number, message: string) {
  const error = new ApiError(status, message);
  error.httpStatus = status;
  return error;
}

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const existingKey: SSHKeyRecord = {
  id: "key-1",
  name: "生产部署密钥",
  username: "root",
  keyType: "ed25519",
  publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA",
  fingerprint: "SHA256:fingerprint",
  broadScope: false,
  disabled: false,
  expiresAt: "",
  allowedPurposes: "",
  allowedNodeIds: "",
  allowedNodeTags: "",
  createdAt: "2026-01-01 00:00:00",
};

const otherKey: SSHKeyRecord = { ...existingKey, id: "key-2", name: "备份密钥" };

type EditorProps = ComponentProps<typeof SSHKeyEditorDialog>;

function editor(props: Partial<EditorProps> & Pick<EditorProps, "onSave">) {
  return (
    <MemoryRouter>
      <SSHKeyEditorDialog open onOpenChange={vi.fn()} editingKey={existingKey} {...props} />
    </MemoryRouter>
  );
}

/**
 * Mirrors the console hook: the editor's owned options drive the shared
 * conditional challenge around the PUT. `outcomes` records what the page-level
 * caller would observe, since it only publishes success when onSave resolves.
 */
function replacementSave(put: (privateKey: string, proof?: string) => Promise<void>) {
  const outcomes: string[] = [];
  const onSave = vi.fn(async (draft: SSHKeyDraft, options: SSHKeyReplacementOptions) => {
    try {
      await runSSHKeyReplacement((proof) => put(draft.privateKey, proof), options);
      outcomes.push("resolved");
    } catch (error) {
      outcomes.push(`rejected:${String((error as { name?: unknown } | null)?.name ?? "unknown")}`);
      throw error;
    }
  });
  return { onSave, outcomes };
}

const privateKeyField = () => screen.getByLabelText(/私钥内容/);
const submitButton = () => document.querySelector<HTMLButtonElement>('button[type="submit"]')!;
const proofSignal = (index: number) => ensureStepUpProof.mock.calls[index]?.[1]?.signal;

type DeferredRead = { aborted: boolean; complete: (text: string) => void; fail: () => void };

function installDelayedFileReader(): DeferredRead[] {
  const pending: DeferredRead[] = [];
  class DelayedFileReader {
    onload: ((ev: ProgressEvent<FileReader>) => unknown) | null = null;
    onerror: ((ev: ProgressEvent<FileReader>) => unknown) | null = null;
    aborted = false;
    abort() {
      this.aborted = true;
    }
    readAsText() {
      pending.push(this);
    }
    complete(text: string) {
      this.onload?.({ target: { result: text } } as unknown as ProgressEvent<FileReader>);
    }
    fail() {
      this.onerror?.(new ProgressEvent("error") as ProgressEvent<FileReader>);
    }
  }
  vi.stubGlobal("FileReader", DelayedFileReader);
  return pending;
}

function changeKeyFile(name: string) {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]')!;
  fireEvent.change(input, { target: { files: [new File(["ignored"], name)] } });
}

describe("SSHKeyEditorDialog behavior", () => {
  beforeEach(() => {
    authRef.current = {
      role: "admin",
      token: "token-1",
      totpEnabled: true,
      authTransitioning: false,
      ensureStepUpProof,
    };
    ensureStepUpProof.mockReset().mockResolvedValue("proof-test");
    toastError.mockReset();
    toastSuccess.mockReset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("编辑已有密钥时展示带有 data-testid='ssh-key-replacement-policy' 的替换策略说明，新建时不展示", () => {
    const { unmount } = render(editor({ onSave: vi.fn() }));

    expect(screen.getByTestId("ssh-key-replacement-policy")).toHaveTextContent(
      "实际替换私钥需要二次验证；普通编辑不要求 SSH 连通，也不会向远端部署公钥。",
    );
    unmount();

    render(editor({ onSave: vi.fn(), editingKey: null }));
    expect(screen.queryByTestId("ssh-key-replacement-policy")).not.toBeInTheDocument();
  });

  it("首次保存成功无需二次验证", async () => {
    const user = userEvent.setup();
    const put = vi.fn().mockResolvedValue(undefined);
    const { onSave, outcomes } = replacementSave(put);
    render(editor({ onSave }));

    await user.click(submitButton());
    await waitFor(() => expect(outcomes).toEqual(["resolved"]));
    expect(put).toHaveBeenCalledTimes(1);
    expect(put).toHaveBeenCalledWith("", undefined);
    expect(ensureStepUpProof).not.toHaveBeenCalled();
  });

  it("真实 403 触发一次 fresh proof，重试成功后完成保存", async () => {
    const user = userEvent.setup();
    const put = vi.fn()
      .mockRejectedValueOnce(createStepUpRequiredError())
      .mockResolvedValueOnce(undefined);
    const { onSave, outcomes } = replacementSave(put);
    ensureStepUpProof.mockResolvedValueOnce("fresh-proof-xyz");
    render(editor({ onSave }));

    await user.type(privateKeyField(), "FAKE_NEW_PRIVATE_KEY");
    await user.click(submitButton());

    await waitFor(() => expect(outcomes).toEqual(["resolved"]));
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProof).toHaveBeenCalledWith(
      "ssh_key.replace",
      expect.objectContaining({ persist: false, reuseCached: false, signal: expect.any(AbortSignal) }),
    );
    expect(put.mock.calls).toEqual([
      ["FAKE_NEW_PRIVATE_KEY", undefined],
      ["FAKE_NEW_PRIVATE_KEY", "fresh-proof-xyz"],
    ]);
  });

  it("防重入锁阻止并发重复点击提交", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<void>();
    const onSave = vi.fn(() => pending.promise);
    render(editor({ onSave }));

    await user.click(submitButton());
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(privateKeyField()).toBeDisabled();

    await user.click(submitButton());
    fireEvent.submit(submitButton().form!);
    expect(onSave).toHaveBeenCalledTimes(1);

    await act(async () => {
      pending.resolve();
    });
    await waitFor(() => expect(submitButton()).toBeEnabled());
  });

  it("用户取消二次验证时保留 #ssh-key-edit-private-key 草稿，不报错且无成功反馈", async () => {
    const user = userEvent.setup();
    const put = vi.fn().mockRejectedValue(createStepUpRequiredError());
    const { onSave, outcomes } = replacementSave(put);
    ensureStepUpProof.mockRejectedValueOnce(new Error("已取消二次验证。"));
    render(editor({ onSave }));

    await user.type(privateKeyField(), "MY_PRIVATE_KEY_CANDIDATE");
    await user.click(submitButton());

    await waitFor(() => expect(outcomes).toEqual(["rejected:SSHKeyReplacementProofError"]));
    expect(put).toHaveBeenCalledTimes(1);
    expect(privateKeyField()).toHaveValue("MY_PRIVATE_KEY_CANDIDATE");
    expect(screen.queryByText(INCOMPLETE_TEXT)).not.toBeInTheDocument();
    expect(screen.queryByText(UNKNOWN_TEXT)).not.toBeInTheDocument();
    expect(toastSuccess).not.toHaveBeenCalled();
    await waitFor(() => expect(submitButton()).toBeEnabled());
  });

  it("二次验证以其他原因失败时显示未继续保存，不续交", async () => {
    const user = userEvent.setup();
    const put = vi.fn().mockRejectedValue(createStepUpRequiredError());
    const { onSave } = replacementSave(put);
    ensureStepUpProof.mockRejectedValueOnce(new Error("验证码错误"));
    render(editor({ onSave }));

    await user.type(privateKeyField(), "CANDIDATE");
    await user.click(submitButton());

    expect(await screen.findByText(INCOMPLETE_TEXT)).toBeInTheDocument();
    expect(screen.queryByText("验证码错误")).not.toBeInTheDocument();
    expect(put).toHaveBeenCalledTimes(1);
    expect(privateKeyField()).toHaveValue("CANDIDATE");
    expect(submitButton()).toBeEnabled();
  });

  it("第二次 403 挑战展示失效提示，不发起第三次调用并保留草稿", async () => {
    const user = userEvent.setup();
    const put = vi.fn().mockRejectedValue(createStepUpRequiredError());
    const { onSave } = replacementSave(put);
    ensureStepUpProof.mockResolvedValueOnce("proof-1");
    render(editor({ onSave }));

    await user.type(privateKeyField(), "TEST_KEY_RESUBMIT");
    await user.click(submitButton());

    expect(await screen.findByText(EXPIRED_TEXT)).toBeInTheDocument();
    expect(put).toHaveBeenCalledTimes(2);
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(privateKeyField()).toHaveValue("TEST_KEY_RESUBMIT");
    expect(submitButton()).toBeEnabled();
  });

  it("HTTP 200 失败信封、普通 403 与网络错误都不弹出挑战也不重放", async () => {
    const user = userEvent.setup();
    const envelopeStepUp = createStepUpRequiredError();
    envelopeStepUp.httpStatus = 200;
    const cases: Array<[unknown, string]> = [
      [new SSHKeyUpdateDecodeError(), UNKNOWN_TEXT],
      [envelopeStepUp, UNKNOWN_TEXT],
      [httpError(403, "forbidden raw"), "当前身份不能修改这把密钥。"],
      [new TypeError("Failed to fetch"), UNKNOWN_TEXT],
    ];
    for (const [failure, visible] of cases) {
      const put = vi.fn().mockRejectedValue(failure);
      const { onSave } = replacementSave(put);
      const view = render(editor({ onSave }));
      await user.type(privateKeyField(), "CANDIDATE");
      await user.click(submitButton());
      expect(await screen.findByText(visible)).toBeInTheDocument();
      expect(put).toHaveBeenCalledTimes(1);
      expect(ensureStepUpProof).not.toHaveBeenCalled();
      expect(screen.queryByText("forbidden raw")).not.toBeInTheDocument();
      view.unmount();
    }
  });

  it("确定性错误 400 / 404 / 403 / 409 / 413 在中英文下只显示固定本地化提示，不透传后端文本，保留草稿可再次显式提交", async () => {
    const user = userEvent.setup();
    const backendInvalid = "请求参数或候选密钥无效";
    const backendBusy = "密钥正在更新，请稍后重试";
    const localized = {
      zh: {
        404: "密钥已不存在，本次修改未保存。",
        403: "当前身份不能修改这把密钥。",
        413: "私钥内容过大，本次修改未保存。",
        409: "密钥名称已被占用或密钥正在更新，本次修改未保存。请稍后重新提交。",
        400: "提交的密钥信息无效，本次修改未保存。请检查后重新提交。",
      },
      en: {
        404: "The key is no longer available. This edit was not saved.",
        403: "The current identity cannot edit this key.",
        413: "The private key is too large. This edit was not saved.",
        409: "The key name is already in use or the key is being updated. This edit was not saved; try submitting again later.",
        400: "The submitted key details are invalid. This edit was not saved; check the fields and submit again.",
      },
    } as const;
    const backendText = {
      404: "not found raw",
      403: "forbidden raw",
      413: "too large raw",
      409: backendBusy,
      400: backendInvalid,
    } as const;
    const field = () => document.querySelector<HTMLTextAreaElement>("#ssh-key-edit-private-key")!;
    const previousLanguage = i18n.language;
    try {
      for (const language of ["zh", "en"] as const) {
        await act(async () => {
          await i18n.changeLanguage(language);
        });
        for (const status of [404, 403, 413, 409, 400] as const) {
          const put = vi.fn()
            .mockRejectedValueOnce(httpError(status, backendText[status]))
            .mockResolvedValueOnce(undefined);
          const { onSave, outcomes } = replacementSave(put);
          const view = render(editor({ onSave }));
          await user.type(field(), "VALID_DRAFT_TEXT");
          await user.click(submitButton());

          expect(await screen.findByText(localized[language][status])).toBeInTheDocument();
          expect(screen.queryByText(/raw/)).not.toBeInTheDocument();
          expect(screen.queryByText(backendInvalid)).not.toBeInTheDocument();
          expect(screen.queryByText(backendBusy)).not.toBeInTheDocument();
          expect(ensureStepUpProof).not.toHaveBeenCalled();
          expect(field()).toHaveValue("VALID_DRAFT_TEXT");
          expect(submitButton()).toBeEnabled();

          await user.click(submitButton());
          await waitFor(() => expect(outcomes[outcomes.length - 1]).toBe("resolved"));
          expect(put.mock.calls).toEqual([
            ["VALID_DRAFT_TEXT", undefined],
            ["VALID_DRAFT_TEXT", undefined],
          ]);
          view.unmount();
        }
      }
    } finally {
      await act(async () => {
        await i18n.changeLanguage(previousLanguage);
      });
    }
  });

  it("网络失败 / 500 / 解码异常进入 UNKNOWN 状态：清空私钥草稿、展示未知结果告警并永久禁用提交", async () => {
    const user = userEvent.setup();
    for (const failure of [new SSHKeyUpdateDecodeError(), httpError(500, "boom"), httpError(429, "slow"), new TypeError("Failed to fetch")]) {
      const put = vi.fn().mockRejectedValue(failure);
      const { onSave } = replacementSave(put);
      const view = render(editor({ onSave }));
      await user.type(privateKeyField(), "SECRET_TO_CLEAR");
      await user.click(submitButton());

      expect(await screen.findByText(UNKNOWN_TEXT)).toBeInTheDocument();
      expect(screen.queryByText("boom")).not.toBeInTheDocument();
      expect(screen.queryByText(/原密钥/)).not.toBeInTheDocument();
      expect(privateKeyField()).toHaveValue("");
      expect(privateKeyField()).toBeDisabled();
      expect(submitButton()).toBeDisabled();
      fireEvent.submit(submitButton().form!);
      expect(put).toHaveBeenCalledTimes(1);
      view.unmount();
    }
  });

  it("TOTP 未启用：元数据保存成功；输入私钥展示前置提示但不禁用提交；真实挑战不请求 proof 也不续交", async () => {
    const user = userEvent.setup();
    authRef.current = { ...authRef.current, totpEnabled: false };
    ensureStepUpProof.mockImplementation(async () => {
      throw new StepUpPrerequisiteError();
    });

    const metadataPut = vi.fn().mockResolvedValue(undefined);
    const metadata = replacementSave(metadataPut);
    const metadataView = render(editor({ onSave: metadata.onSave }));
    expect(screen.queryByText(/需先启用两步验证/)).not.toBeInTheDocument();
    await user.click(submitButton());
    await waitFor(() => expect(metadata.outcomes).toEqual(["resolved"]));
    metadataView.unmount();

    const put = vi.fn().mockRejectedValue(createStepUpRequiredError());
    const { onSave, outcomes } = replacementSave(put);
    render(editor({ onSave }));
    await user.type(privateKeyField(), "CANDIDATE_KEY");
    expect(await screen.findByText(/需先启用两步验证/)).toBeInTheDocument();
    expect(submitButton()).toBeEnabled();

    await user.click(submitButton());
    await waitFor(() => expect(outcomes).toEqual(["rejected:SSHKeyReplacementProofError"]));
    expect(put).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/需先启用两步验证/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /启用/ })).toHaveAttribute("href", "/app/settings?tab=account");
    expect(privateKeyField()).toHaveValue("CANDIDATE_KEY");
    expect(screen.queryByText(INCOMPLETE_TEXT)).not.toBeInTheDocument();
  });

  it("等待二次验证时关闭会中止验证，迟到的 proof 不续交，重开后是空草稿", async () => {
    const user = userEvent.setup();
    const put = vi.fn().mockRejectedValueOnce(createStepUpRequiredError());
    const { onSave, outcomes } = replacementSave(put);
    const proof = createDeferred<string>();
    ensureStepUpProof.mockImplementationOnce(() => proof.promise);
    const view = render(editor({ onSave }));

    await user.type(privateKeyField(), "CANDIDATE_A");
    await user.click(submitButton());
    await waitFor(() => expect(ensureStepUpProof).toHaveBeenCalledTimes(1));

    view.rerender(editor({ onSave, open: false }));
    expect(proofSignal(0)?.aborted).toBe(true);
    await act(async () => {
      proof.resolve("late-proof");
    });
    expect(put).toHaveBeenCalledTimes(1);
    expect(outcomes).toEqual(["rejected:AbortError"]);

    view.rerender(editor({ onSave, open: true }));
    expect(privateKeyField()).toHaveValue("");
    expect(submitButton()).toBeEnabled();
    expect(screen.queryByText(INCOMPLETE_TEXT)).not.toBeInTheDocument();
  });

  it("认证转换期间中止验证，迟到的 proof 拒绝不显示错误也不续交", async () => {
    const user = userEvent.setup();
    const put = vi.fn().mockRejectedValueOnce(createStepUpRequiredError());
    const { onSave, outcomes } = replacementSave(put);
    const proof = createDeferred<string>();
    ensureStepUpProof.mockImplementationOnce(() => proof.promise);
    const view = render(editor({ onSave }));

    await user.type(privateKeyField(), "CANDIDATE_A");
    await user.click(submitButton());
    await waitFor(() => expect(ensureStepUpProof).toHaveBeenCalledTimes(1));

    authRef.current = { ...authRef.current, authTransitioning: true };
    view.rerender(editor({ onSave }));
    expect(proofSignal(0)?.aborted).toBe(true);
    await act(async () => {
      proof.reject(new Error("验证码错误"));
    });
    authRef.current = { ...authRef.current, authTransitioning: false };
    view.rerender(editor({ onSave }));

    expect(put).toHaveBeenCalledTimes(1);
    expect(outcomes).toEqual(["rejected:AbortError"]);
    expect(screen.queryByText(INCOMPLETE_TEXT)).not.toBeInTheDocument();
    expect(privateKeyField()).toHaveValue("");
    expect(submitButton()).toBeEnabled();
  });

  it("切换密钥后旧 PUT 迟到失败不污染新表单，旧 finally 不释放新提交锁", async () => {
    const user = userEvent.setup();
    const oldPut = createDeferred<void>();
    const newPut = createDeferred<void>();
    const put = vi.fn()
      .mockReturnValueOnce(oldPut.promise)
      .mockReturnValueOnce(newPut.promise);
    const { onSave, outcomes } = replacementSave(put);
    const view = render(editor({ onSave }));

    await user.type(privateKeyField(), "CANDIDATE_A");
    await user.click(submitButton());
    expect(put).toHaveBeenCalledTimes(1);

    view.rerender(editor({ onSave, editingKey: otherKey }));
    expect(privateKeyField()).toHaveValue("");
    await user.type(privateKeyField(), "CANDIDATE_B");
    await user.click(submitButton());
    expect(put).toHaveBeenLastCalledWith("CANDIDATE_B", undefined);

    await act(async () => {
      oldPut.reject(new TypeError("Failed to fetch"));
    });
    expect(outcomes).toEqual(["rejected:AbortError"]);
    expect(screen.queryByText(UNKNOWN_TEXT)).not.toBeInTheDocument();
    expect(privateKeyField()).toHaveValue("CANDIDATE_B");
    expect(submitButton()).toBeDisabled();
    fireEvent.submit(submitButton().form!);
    expect(put).toHaveBeenCalledTimes(2);

    await act(async () => {
      newPut.resolve();
    });
    await waitFor(() => expect(outcomes).toEqual(["rejected:AbortError", "resolved"]));
    await waitFor(() => expect(submitButton()).toBeEnabled());
  });

  it("同 token 认证代次变化后，续交的迟到成功或失败都不能发布", async () => {
    const user = userEvent.setup();
    for (const settle of ["resolve", "reject"] as const) {
      const retry = createDeferred<void>();
      const put = vi.fn()
        .mockRejectedValueOnce(createStepUpRequiredError())
        .mockReturnValueOnce(retry.promise);
      const { onSave, outcomes } = replacementSave(put);
      const view = render(editor({ onSave }));

      await user.type(privateKeyField(), "CANDIDATE_A");
      await user.click(submitButton());
      await waitFor(() => expect(put).toHaveBeenCalledTimes(2));

      bumpAuthSessionGeneration();
      view.rerender(editor({ onSave }));
      await act(async () => {
        if (settle === "resolve") retry.resolve();
        else retry.reject(httpError(500, "boom"));
      });

      expect(outcomes).toEqual(["rejected:AbortError"]);
      expect(screen.queryByText(UNKNOWN_TEXT)).not.toBeInTheDocument();
      expect(privateKeyField()).toHaveValue("");
      expect(privateKeyField()).toBeEnabled();
      expect(submitButton()).toBeEnabled();
      view.unmount();
    }
  });

  it("令牌 A-B-A 后旧 PUT 的确定拒绝不显示，新的显式提交仍可完成", async () => {
    const user = userEvent.setup();
    const oldPut = createDeferred<void>();
    const put = vi.fn().mockReturnValueOnce(oldPut.promise).mockResolvedValueOnce(undefined);
    const { onSave, outcomes } = replacementSave(put);
    const view = render(editor({ onSave }));

    await user.type(privateKeyField(), "CANDIDATE_A");
    await user.click(submitButton());

    authRef.current = { ...authRef.current, token: "token-2" };
    view.rerender(editor({ onSave }));
    authRef.current = { ...authRef.current, token: "token-1" };
    view.rerender(editor({ onSave }));
    await act(async () => {
      oldPut.reject(httpError(404, "not found raw"));
    });

    expect(outcomes).toEqual(["rejected:AbortError"]);
    expect(screen.queryByText("密钥已不存在，本次修改未保存。")).not.toBeInTheDocument();
    expect(privateKeyField()).toHaveValue("");

    await user.type(privateKeyField(), "CANDIDATE_FRESH");
    await user.click(submitButton());
    await waitFor(() => expect(outcomes).toEqual(["rejected:AbortError", "resolved"]));
    expect(put).toHaveBeenLastCalledWith("CANDIDATE_FRESH", undefined);
  });

  it("卸载后中止验证，迟到的 proof 与 PUT 都不续交也不报错", async () => {
    const user = userEvent.setup();
    const proof = createDeferred<string>();
    const put = vi.fn().mockRejectedValueOnce(createStepUpRequiredError());
    const first = replacementSave(put);
    ensureStepUpProof.mockImplementationOnce(() => proof.promise);
    const proofView = render(editor({ onSave: first.onSave }));
    await user.type(privateKeyField(), "CANDIDATE_A");
    await user.click(submitButton());
    await waitFor(() => expect(ensureStepUpProof).toHaveBeenCalledTimes(1));
    proofView.unmount();
    expect(proofSignal(0)?.aborted).toBe(true);
    await act(async () => {
      proof.resolve("late-proof");
    });
    expect(put).toHaveBeenCalledTimes(1);
    expect(first.outcomes).toEqual(["rejected:AbortError"]);

    const pendingPut = createDeferred<void>();
    const second = replacementSave(vi.fn().mockReturnValueOnce(pendingPut.promise));
    const putView = render(editor({ onSave: second.onSave }));
    await user.type(privateKeyField(), "CANDIDATE_B");
    await user.click(submitButton());
    putView.unmount();
    await act(async () => {
      pendingPut.reject(new TypeError("Failed to fetch"));
    });
    expect(second.outcomes).toEqual(["rejected:AbortError"]);
    expect(toastError).not.toHaveBeenCalled();
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("关闭重开、切换密钥或新上传后，旧文件读取不能写入当前表单，当前读取仍生效", () => {
    const pending = installDelayedFileReader();
    const onSave = vi.fn();
    const view = render(editor({ onSave }));

    changeKeyFile("closed.pem");
    view.rerender(editor({ onSave, open: false }));
    expect(pending[0].aborted).toBe(true);
    view.rerender(editor({ onSave, open: true }));
    act(() => {
      pending[0].complete("LATE_CLOSED_KEY");
      pending[0].fail();
    });
    expect(privateKeyField()).toHaveValue("");

    changeKeyFile("switched.pem");
    view.rerender(editor({ onSave, editingKey: otherKey }));
    expect(pending[1].aborted).toBe(true);
    act(() => {
      pending[1].complete("LATE_SWITCHED_KEY");
      pending[1].fail();
    });
    expect(privateKeyField()).toHaveValue("");

    changeKeyFile("replaced.pem");
    changeKeyFile("current.pem");
    expect(pending[2].aborted).toBe(true);
    act(() => {
      pending[2].complete("REPLACED_KEY");
      pending[2].fail();
    });
    expect(privateKeyField()).toHaveValue("");
    expect(toastError).not.toHaveBeenCalled();

    act(() => pending[3].complete("CURRENT_KEY"));
    expect(privateKeyField()).toHaveValue("CURRENT_KEY");
  });

  it("提交时撤销未完成的文件读取：等待二次验证期间读取完成不能替换显示中的候选，续交仍使用捕获的候选", async () => {
    const user = userEvent.setup();
    const pending = installDelayedFileReader();
    const put = vi.fn()
      .mockRejectedValueOnce(createStepUpRequiredError())
      .mockResolvedValueOnce(undefined);
    const { onSave, outcomes } = replacementSave(put);
    const proof = createDeferred<string>();
    ensureStepUpProof.mockImplementationOnce(() => proof.promise);
    render(editor({ onSave }));

    await user.type(privateKeyField(), "CANDIDATE_A");
    changeKeyFile("candidate-b.pem");
    await user.click(submitButton());
    expect(pending[0].aborted).toBe(true);
    await waitFor(() => expect(ensureStepUpProof).toHaveBeenCalledTimes(1));

    act(() => {
      pending[0].complete("FILE_CANDIDATE_B");
      pending[0].fail();
    });
    expect(privateKeyField()).toHaveValue("CANDIDATE_A");
    expect(toastError).not.toHaveBeenCalled();

    await act(async () => {
      proof.resolve("fresh-proof");
    });
    await waitFor(() => expect(outcomes).toEqual(["resolved"]));
    expect(put.mock.calls).toEqual([
      ["CANDIDATE_A", undefined],
      ["CANDIDATE_A", "fresh-proof"],
    ]);
    expect(privateKeyField()).toHaveValue("CANDIDATE_A");
  });

  it("结果未知清空私钥后，迟到完成的文件读取不能重新填入秘密", async () => {
    const user = userEvent.setup();
    const pending = installDelayedFileReader();
    const pendingPut = createDeferred<void>();
    const put = vi.fn().mockReturnValueOnce(pendingPut.promise);
    const { onSave, outcomes } = replacementSave(put);
    render(editor({ onSave }));

    await user.type(privateKeyField(), "SECRET_A");
    changeKeyFile("candidate-b.pem");
    await user.click(submitButton());
    expect(pending[0].aborted).toBe(true);

    await act(async () => {
      pendingPut.reject(httpError(500, "boom"));
    });
    expect(await screen.findByText(UNKNOWN_TEXT)).toBeInTheDocument();
    expect(outcomes).toEqual(["rejected:ApiError"]);
    expect(privateKeyField()).toHaveValue("");

    act(() => {
      pending[0].complete("FILE_SECRET_B");
      pending[0].fail();
    });
    expect(privateKeyField()).toHaveValue("");
    expect(privateKeyField()).toBeDisabled();
    expect(submitButton()).toBeDisabled();
    expect(toastError).not.toHaveBeenCalled();
    expect(put).toHaveBeenCalledTimes(1);
  });
});
