import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTOTPApi, classifyRecoveryCodeRegenerationFailure, classifyTOTPVerifyFailure, mapTOTPVerifyResponse, RecoveryCodeContractError, STEP_UP_ACTIONS, TOTPVerifyContractError } from "./totp-api";
import { ApiError, AuthTransitionRejectedError, beginAuthTransitionBarrier, clearAuthTransitionBarrier } from "./core";

function createMockResponse(body: unknown) {
  return {
    status: 200,
    ok: true,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify({ code: 0, message: "ok", data: body })),
  } as unknown as Response;
}

function createStatusResponse(status: number, payload: unknown) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify(payload)),
  } as unknown as Response;
}

function recoveryApiError(status: number, code: string, httpStatus: number = status) {
  const error = new ApiError(status, "bad", { code: status, message: "bad", data: { error_code: code } });
  error.httpStatus = httpStatus;
  return error;
}

describe("totp-api step-up", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue(createMockResponse({
      proof: "FAKE_PROOF_FOR_TEST_ONLY",
      expires_at: "2026-07-13T06:00:00Z",
      proof_ttl_seconds: 300,
    }));
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("sends the exact step_up_action with proof issuance", async () => {
    const api = createTOTPApi();
    await api.requestStepUpProof("FAKE_AUTH_TOKEN_FOR_TEST_ONLY", "123456", STEP_UP_ACTIONS.terminalOpen);

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/step-up", {
      method: "POST",
      headers: {
        Authorization: "Bearer FAKE_AUTH_TOKEN_FOR_TEST_ONLY",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ code: "123456", step_up_action: "terminal.open" }),
      signal: undefined,
      cache: undefined,
    });
  });

  it("maps step-up proof wire fields to camelCase", async () => {
    const api = createTOTPApi();
    await expect(api.requestStepUpProof("token", "123456", STEP_UP_ACTIONS.terminalOpen)).resolves.toEqual({
      proof: "FAKE_PROOF_FOR_TEST_ONLY",
      expiresAt: "2026-07-13T06:00:00Z",
      proofTtlSeconds: 300,
    });
  });

  it("forwards the owner signal so an aborted proof POST rejects without mapping a late body", async () => {
    fetchMock.mockReset();
    fetchMock.mockImplementationOnce((_url: string, init: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    }));
    const controller = new AbortController();
    const api = createTOTPApi();

    const pending = api.requestStepUpProof("token", "123456", STEP_UP_ACTIONS.systemBackupDownload, {
      signal: controller.signal,
    });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect((fetchMock.mock.calls[0] as [string, RequestInit])[1].signal).toBe(controller.signal);
    controller.abort();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });
});

describe("totp-api enrollment", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    vi.unstubAllGlobals();
  });

  it("maps setup enrollment_id and expires_at", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      secret: "FAKESECRET_s4t5u6v7w8x9y0z1a2b3",
      qr_url: "otpauth://totp/xirang",
      issuer: "xirang",
      enrollment_id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      expires_at: "2026-09-08T12:00:00Z",
    }));
    const api = createTOTPApi();
    await expect(api.totpSetup("token-1")).resolves.toEqual({
      secret: "FAKESECRET_s4t5u6v7w8x9y0z1a2b3",
      qrUrl: "otpauth://totp/xirang",
      issuer: "xirang",
      enrollmentId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      expiresAt: "2026-09-08T12:00:00Z",
    });
  });

  it("verify 必须带 enrollment_id，缺失时不发请求", async () => {
    const api = createTOTPApi();
    await expect(api.totpVerify("token-1", "123456", "  ", 1)).rejects.toThrow("enrollment_id is required");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("verify 请求 JSON 包含 code 和 enrollment_id，并严格映射换发会话", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      token: "replacement-token",
      user: { id: 7, username: "alice", role: "admin", totp_enabled: true },
      recovery_codes: ["aaaa-bbbb"],
    }));
    const api = createTOTPApi();
    await expect(api.totpVerify("token-1", "123456", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 4)).resolves.toEqual({
      token: "replacement-token",
      user: { id: 7, username: "alice", role: "admin", totpEnabled: true },
      recoveryCodes: ["aaaa-bbbb"],
    });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/2fa/verify", {
      method: "POST",
      headers: {
        Authorization: "Bearer token-1",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ code: "123456", enrollment_id: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }),
      signal: undefined,
      cache: undefined,
    });
  });

  it("启用阻断只放行匹配的 verify", async () => {
    const id = beginAuthTransitionBarrier();
    const api = createTOTPApi();
    await expect(api.totpVerify("token-1", "123456", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", id + 1)).rejects.toBeInstanceOf(AuthTransitionRejectedError);
    expect(fetchMock).not.toHaveBeenCalled();
    fetchMock.mockResolvedValueOnce(createMockResponse({
      token: "replacement-token",
      user: { id: 7, username: "alice", role: "operator", totp_enabled: true },
      recovery_codes: ["cccc-dddd"],
    }));
    await expect(api.totpVerify("token-1", "123456", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", id)).resolves.toMatchObject({
      token: "replacement-token",
      user: { role: "operator", totpEnabled: true },
    });
    clearAuthTransitionBarrier();
  });
});

describe("totp verify contract", () => {
  const valid = {
    token: "replacement-token",
    user: { id: 7, username: "alice", role: "admin", totp_enabled: true },
    recovery_codes: ["aaaa-bbbb"],
  };

  it("拒绝布尔强制转换、缺 token、坏角色和非法恢复码", () => {
    expect(() => mapTOTPVerifyResponse({ ...valid, user: { ...valid.user, totp_enabled: "true" } })).toThrow(TOTPVerifyContractError);
    expect(() => mapTOTPVerifyResponse({ ...valid, user: { ...valid.user, totp_enabled: 1 } })).toThrow(TOTPVerifyContractError);
    expect(() => mapTOTPVerifyResponse({ ...valid, token: "  " })).toThrow(TOTPVerifyContractError);
    expect(() => mapTOTPVerifyResponse({ ...valid, user: { ...valid.user, role: "root" } })).toThrow(TOTPVerifyContractError);
    expect(() => mapTOTPVerifyResponse({ ...valid, user: { ...valid.user, id: "7" } })).toThrow(TOTPVerifyContractError);
    expect(() => mapTOTPVerifyResponse({ ...valid, recovery_codes: [1] })).toThrow(TOTPVerifyContractError);
    expect(() => mapTOTPVerifyResponse({ ...valid, recovery_codes: [] })).toThrow(TOTPVerifyContractError);
  });

  it("只把确定未提交的错误码当成可重试，5xx 和未知码保持不确定", () => {
    expect(classifyTOTPVerifyFailure(new ApiError(400, "bad", { data: { error_code: "TOTP_CODE_INVALID" } }))).toBe("no_commit");
    expect(classifyTOTPVerifyFailure(new ApiError(400, "bad", { data: { error_code: "TOTP_ENROLLMENT_EXPIRED" } }))).toBe("no_commit");
    expect(classifyTOTPVerifyFailure(new ApiError(400, "bad", { data: { error_code: "TOTP_ALREADY_ENABLED" } }))).toBe("ambiguous");
    expect(classifyTOTPVerifyFailure(new ApiError(500, "bad", { data: { error_code: "TOTP_CODE_INVALID" } }))).toBe("ambiguous");
    expect(classifyTOTPVerifyFailure(new ApiError(400, "bad", { data: {} }))).toBe("ambiguous");
    expect(classifyTOTPVerifyFailure(new TypeError("Failed to fetch"))).toBe("ambiguous");
  });
});

describe("recovery code management", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("reads a nonnegative safe integer and does not return inventory material", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      remaining: 8,
      recovery_codes: ["should-not-surface"],
      totp_secret: "should-not-surface",
    }));
    const api = createTOTPApi();
    await expect(api.recoveryCodesRemaining("token-1")).resolves.toEqual({ remaining: 8 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/2fa/recovery-codes", {
      method: "GET",
      headers: { Authorization: "Bearer token-1" },
      body: undefined,
      signal: undefined,
      cache: "no-cache",
    });

    fetchMock.mockResolvedValueOnce(createMockResponse({ remaining: 0 }));
    await expect(api.recoveryCodesRemaining("token-1")).resolves.toEqual({ remaining: 0 });
    fetchMock.mockResolvedValueOnce(createMockResponse({ remaining: Number.MAX_SAFE_INTEGER }));
    await expect(api.recoveryCodesRemaining("token-1")).resolves.toEqual({ remaining: Number.MAX_SAFE_INTEGER });
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it.each<[string, unknown]>([
    ["missing", {}],
    ["null payload", null],
    ["string", { remaining: "8" }],
    ["numeric string zero", { remaining: "0" }],
    ["empty string", { remaining: "" }],
    ["negative", { remaining: -1 }],
    ["float", { remaining: 1.5 }],
    ["boolean", { remaining: true }],
    ["null", { remaining: null }],
    ["array", { remaining: [8] }],
    ["unsafe integer", { remaining: 9007199254740992 }],
  ])("rejects %s remaining instead of coercing it", async (_label, data) => {
    fetchMock.mockResolvedValueOnce(createMockResponse(data));
    const api = createTOTPApi();
    await expect(api.recoveryCodesRemaining("token-1")).rejects.toBeInstanceOf(RecoveryCodeContractError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("rejects NaN and Infinity remaining", async () => {
    const parse = JSON.parse;
    const spy = vi.spyOn(JSON, "parse").mockImplementation((text: string) => {
      const value = parse(text) as { data?: { remaining?: unknown } };
      if (value?.data) {
        value.data.remaining = Number.NaN;
      }
      return value;
    });
    fetchMock.mockResolvedValueOnce(createMockResponse({ remaining: 1 }));
    const api = createTOTPApi();
    await expect(api.recoveryCodesRemaining("token-1")).rejects.toBeInstanceOf(RecoveryCodeContractError);

    spy.mockImplementation((text: string) => {
      const value = parse(text) as { data?: { remaining?: unknown } };
      if (value?.data) {
        value.data.remaining = Number.POSITIVE_INFINITY;
      }
      return value;
    });
    fetchMock.mockResolvedValueOnce(createMockResponse({ remaining: 1 }));
    await expect(api.recoveryCodesRemaining("token-1")).rejects.toBeInstanceOf(RecoveryCodeContractError);
  });

  it("does not turn an error envelope into a zero inventory", async () => {
    fetchMock.mockResolvedValueOnce(createStatusResponse(200, {
      code: 409,
      message: "disabled",
      data: { error_code: "RECOVERY_CODE_TOTP_DISABLED", remaining: 0 },
    }));
    const api = createTOTPApi();
    const error = await api.recoveryCodesRemaining("token-1").then(() => {
      throw new Error("expected rejection");
    }, (caught: unknown) => caught);
    expect(error).toBeInstanceOf(ApiError);
    if (!(error instanceof ApiError)) {
      throw new Error("expected ApiError");
    }
    expect(error.status).toBe(409);
    expect(error.httpStatus).toBe(200);
    expect(classifyRecoveryCodeRegenerationFailure(error)).toBe("ambiguous");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("posts the verifier code once and returns only the new list", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      recovery_codes: ["abcd-efgh", "ijkl-mnop"],
      token: "should-not-surface",
      remaining: 8,
    }));
    const api = createTOTPApi();
    await expect(api.regenerateRecoveryCodes("token-1", " 654321 ")).resolves.toEqual({
      recoveryCodes: ["abcd-efgh", "ijkl-mnop"],
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/2fa/recovery-codes/regenerate", {
      method: "POST",
      headers: {
        Authorization: "Bearer token-1",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ code: " 654321 " }),
      signal: undefined,
      cache: undefined,
    });
  });

  it.each<[string, unknown]>([
    ["empty", { recovery_codes: [] }],
    ["empty string", { recovery_codes: [""] }],
    ["blank mixed", { recovery_codes: ["abcd-efgh", ""] }],
    ["number", { recovery_codes: [1] }],
    ["null item", { recovery_codes: [null] }],
    ["string", { recovery_codes: "abcd-efgh" }],
    ["null", { recovery_codes: null }],
    ["missing", {}],
    ["null payload", null],
  ])("rejects %s recovery list without another post", async (_label, data) => {
    fetchMock.mockResolvedValueOnce(createMockResponse(data));
    const api = createTOTPApi();
    const error = await api.regenerateRecoveryCodes("token-1", "123456").then(() => {
      throw new Error("expected rejection");
    }, (caught: unknown) => caught);
    expect(error).toBeInstanceOf(RecoveryCodeContractError);
    expect(classifyRecoveryCodeRegenerationFailure(error)).toBe("ambiguous");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("does not retry a lost regenerate and allows a later explicit post", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    fetchMock.mockResolvedValueOnce(createMockResponse({ recovery_codes: ["abcd-efgh"] }));
    const api = createTOTPApi();
    const error = await api.regenerateRecoveryCodes("token-1", "111111").then(() => {
      throw new Error("expected rejection");
    }, (caught: unknown) => caught);
    expect(error).toBeInstanceOf(TypeError);
    expect(classifyRecoveryCodeRegenerationFailure(error)).toBe("ambiguous");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await expect(api.regenerateRecoveryCodes("token-1", "222222")).resolves.toEqual({
      recoveryCodes: ["abcd-efgh"],
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("does not use the activation transition to bypass the auth barrier", async () => {
    beginAuthTransitionBarrier();
    const api = createTOTPApi();
    await expect(api.recoveryCodesRemaining("token-1")).rejects.toBeInstanceOf(AuthTransitionRejectedError);
    await expect(api.regenerateRecoveryCodes("token-1", "123456")).rejects.toBeInstanceOf(AuthTransitionRejectedError);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each<[number, string]>([
    [403, "RECOVERY_CODE_TOTP_INVALID"],
    [409, "RECOVERY_CODE_TOTP_DISABLED"],
    [409, "RECOVERY_CODE_CONFLICT"],
  ])("classifies real HTTP %i %s as no_commit", async (status, code) => {
    fetchMock.mockResolvedValueOnce(createStatusResponse(status, {
      code: status,
      message: "rejected",
      data: { error_code: code },
    }));
    const api = createTOTPApi();
    const error = await api.regenerateRecoveryCodes("token-1", "123456").then(() => {
      throw new Error("expected rejection");
    }, (caught: unknown) => caught);
    expect(error).toBeInstanceOf(ApiError);
    if (!(error instanceof ApiError)) {
      throw new Error("expected ApiError");
    }
    expect(error.status).toBe(status);
    expect(error.httpStatus).toBe(status);
    expect(classifyRecoveryCodeRegenerationFailure(error)).toBe("no_commit");
    expect(classifyTOTPVerifyFailure(error)).toBe("ambiguous");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it.each<[number, string]>([
    [403, "RECOVERY_CODE_TOTP_INVALID"],
    [409, "RECOVERY_CODE_CONFLICT"],
    [409, "RECOVERY_CODE_TOTP_DISABLED"],
  ])("keeps HTTP 200 envelope code %i %s ambiguous", async (envelopeCode, code) => {
    fetchMock.mockResolvedValueOnce(createStatusResponse(200, {
      code: envelopeCode,
      message: "malformed",
      data: { error_code: code },
    }));
    const api = createTOTPApi();
    const error = await api.regenerateRecoveryCodes("token-1", "123456").then(() => {
      throw new Error("expected rejection");
    }, (caught: unknown) => caught);
    expect(error).toBeInstanceOf(ApiError);
    if (!(error instanceof ApiError)) {
      throw new Error("expected ApiError");
    }
    expect(error.status).toBe(envelopeCode);
    expect(error.httpStatus).toBe(200);
    expect(classifyRecoveryCodeRegenerationFailure(error)).toBe("ambiguous");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("keeps a real 500 that repeats a no-commit code ambiguous", async () => {
    fetchMock.mockResolvedValueOnce(createStatusResponse(500, {
      code: 500,
      message: "unavailable",
      data: { error_code: "RECOVERY_CODE_CONFLICT" },
    }));
    const api = createTOTPApi();
    const error = await api.regenerateRecoveryCodes("token-1", "123456").then(() => {
      throw new Error("expected rejection");
    }, (caught: unknown) => caught);
    expect(error).toBeInstanceOf(ApiError);
    if (!(error instanceof ApiError)) {
      throw new Error("expected ApiError");
    }
    expect(error.httpStatus).toBe(500);
    expect(classifyRecoveryCodeRegenerationFailure(error)).toBe("ambiguous");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("accepts only an ApiError whose HTTP status matches one of the three codes", () => {
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(403, "RECOVERY_CODE_TOTP_INVALID"))).toBe("no_commit");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(409, "RECOVERY_CODE_TOTP_DISABLED"))).toBe("no_commit");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(409, "RECOVERY_CODE_CONFLICT"))).toBe("no_commit");

    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(403, "RECOVERY_CODE_TOTP_INVALID", 200))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(new ApiError(403, "bad", { data: { error_code: "RECOVERY_CODE_TOTP_INVALID" } }))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(403, "RECOVERY_CODE_CONFLICT"))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(403, "RECOVERY_CODE_TOTP_DISABLED"))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(409, "RECOVERY_CODE_TOTP_INVALID"))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(400, "RECOVERY_CODE_TOTP_INVALID"))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(401, "RECOVERY_CODE_TOTP_INVALID"))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(429, "RECOVERY_CODE_CONFLICT"))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(500, "RECOVERY_CODE_TOTP_INVALID"))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(409, "RECOVERY_CODE_TOTP_INVALID "))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(recoveryApiError(403, "recovery_code_totp_invalid"))).toBe("ambiguous");

    const wrongDetail = new ApiError(403, "bad", { error_code: "RECOVERY_CODE_TOTP_INVALID" });
    wrongDetail.httpStatus = 403;
    expect(classifyRecoveryCodeRegenerationFailure(wrongDetail)).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure({
      status: 403,
      httpStatus: 403,
      detail: { data: { error_code: "RECOVERY_CODE_TOTP_INVALID" } },
    })).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(new TypeError("Failed to fetch"))).toBe("ambiguous");
    expect(classifyRecoveryCodeRegenerationFailure(new RecoveryCodeContractError())).toBe("ambiguous");

    const activation = new ApiError(400, "bad", { data: { error_code: "TOTP_CODE_INVALID" } });
    expect(classifyTOTPVerifyFailure(activation)).toBe("no_commit");
    expect(classifyRecoveryCodeRegenerationFailure(activation)).toBe("ambiguous");
    activation.httpStatus = 400;
    expect(classifyTOTPVerifyFailure(activation)).toBe("no_commit");
    expect(classifyRecoveryCodeRegenerationFailure(activation)).toBe("ambiguous");
    expect(classifyTOTPVerifyFailure(recoveryApiError(403, "RECOVERY_CODE_TOTP_INVALID"))).toBe("ambiguous");
  });
});
