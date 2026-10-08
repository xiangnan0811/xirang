import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTOTPApi, classifyTOTPVerifyFailure, mapTOTPVerifyResponse, STEP_UP_ACTIONS, TOTPVerifyContractError } from "./totp-api";
import { ApiError, AuthTransitionRejectedError, beginAuthTransitionBarrier, clearAuthTransitionBarrier } from "./core";

function createMockResponse(body: unknown) {
  return {
    status: 200,
    ok: true,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify({ code: 0, message: "ok", data: body })),
  } as unknown as Response;
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
