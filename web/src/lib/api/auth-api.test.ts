import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { classifyRecoveryEnrollmentFailure, createAuthApi, parseRFC3339, RecoveryEnrollmentError } from "./auth-api";
import { ApiError, bumpAuthSessionGeneration, getAuthSessionGeneration, subscribeAuthSessionGeneration } from "./core";

function createMockResponse(status = 200, body: unknown = {}) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(
      JSON.stringify({ code: 0, message: "ok", data: body }),
    ),
  } as unknown as Response;
}

describe("auth-api getCaptcha mapping", () => {
  const fetchMock = vi.fn();
  const api = createAuthApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps disabled captcha without primary challenge fields", async () => {
    fetchMock.mockResolvedValue(
      createMockResponse(200, {
        enabled: false,
        second_required: false,
        // If a buggy server still emits these, mapper must drop them.
        id: "should-ignore",
        question: "1 + 1 = ?",
      }),
    );

    await expect(api.getCaptcha()).resolves.toEqual({
      enabled: false,
      id: undefined,
      question: undefined,
      secondRequired: false,
      secondId: undefined,
      secondQuestion: undefined,
    });
  });

  it("maps primary-only captcha when enabled", async () => {
    fetchMock.mockResolvedValue(
      createMockResponse(200, {
        enabled: true,
        id: "cap-1",
        question: "3 + 4 = ?",
        second_required: false,
      }),
    );

    await expect(api.getCaptcha()).resolves.toEqual({
      enabled: true,
      id: "cap-1",
      question: "3 + 4 = ?",
      secondRequired: false,
      secondId: undefined,
      secondQuestion: undefined,
    });
  });

  it("maps second-only captcha without primary fields", async () => {
    fetchMock.mockResolvedValue(
      createMockResponse(200, {
        enabled: false,
        second_required: true,
        second_id: "cap-2",
        second_question: "5 + 6 = ?",
      }),
    );

    await expect(api.getCaptcha()).resolves.toEqual({
      enabled: false,
      id: undefined,
      question: undefined,
      secondRequired: true,
      secondId: "cap-2",
      secondQuestion: "5 + 6 = ?",
    });
  });

  it("maps dual captcha when both channels enabled", async () => {
    fetchMock.mockResolvedValue(
      createMockResponse(200, {
        enabled: true,
        id: "cap-a",
        question: "1 + 2 = ?",
        second_required: true,
        second_id: "cap-b",
        second_question: "7 + 8 = ?",
      }),
    );

    await expect(api.getCaptcha()).resolves.toEqual({
      enabled: true,
      id: "cap-a",
      question: "1 + 2 = ?",
      secondRequired: true,
      secondId: "cap-b",
      secondQuestion: "7 + 8 = ?",
    });
  });
});

describe("auth-api login mapping", () => {
  const fetchMock = vi.fn();
  const api = createAuthApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps 2FA challenge fields to camelCase", async () => {
    fetchMock.mockResolvedValueOnce({
      status: 200,
      ok: true,
      headers: { get: vi.fn().mockReturnValue(null) },
      text: vi.fn().mockResolvedValue(JSON.stringify({
        code: 0,
        message: "ok",
        data: { requires_2fa: true, login_token: "temp" },
      })),
    } as unknown as Response);

    await expect(api.login("admin", "secret")).resolves.toEqual({
      token: undefined,
      user: undefined,
      requires2FA: true,
      loginToken: "temp",
    });
  });
});

const RECOVERY_TOKEN = "aaa.bbb.ccc";
const RECOVERY_SECRET = "JBSWY3DPEHPK3PXP";
const RECOVERY_QR = `otpauth://totp/%E6%81%AF%E5%A3%A4%20XiRang:admin?algorithm=SHA1&digits=6&issuer=%E6%81%AF%E5%A3%A4+XiRang&period=30&secret=${RECOVERY_SECRET}`;

function recoveryLoginData(overrides: Record<string, unknown> = {}, enrollmentOverrides: Record<string, unknown> = {}) {
  return {
    requires_reenrollment: true,
    recovery_token: RECOVERY_TOKEN,
    enrollment: {
      secret: RECOVERY_SECRET,
      qr_url: RECOVERY_QR,
      issuer: "息壤 XiRang",
      enrollment_id: "0123abcd",
      expires_at: "2026-10-16T12:10:00.123456789+08:00",
      ...enrollmentOverrides,
    },
    ...overrides,
  };
}

function envelopeResponse(status: number, envelope: unknown) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify(envelope)),
  } as unknown as Response;
}

const EIGHT_CODES = ["AAAA1111", "BBBB2222", "CCCC3333", "DDDD4444", "EEEE5555", "FFFF6666", "GGGG7777", "HHHH8888"];

describe("auth-api recovery login branch", () => {
  const fetchMock = vi.fn();
  const api = createAuthApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps the exact recovery shape without exposing a session token", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, recoveryLoginData()));

    const result = await api.login("admin", "secret");

    expect(result).toEqual({
      recoveryEnrollment: {
        recoveryToken: RECOVERY_TOKEN,
        secret: RECOVERY_SECRET,
        qrUrl: RECOVERY_QR,
        issuer: "息壤 XiRang",
        enrollmentId: "0123abcd",
        expiresAt: "2026-10-16T12:10:00.123456789+08:00",
        expiresAtMs: Date.UTC(2026, 9, 16, 4, 10, 0, 123),
      },
    });
    expect(result.token).toBeUndefined();
    expect(result.user).toBeUndefined();
    expect(result.requires2FA).toBeUndefined();
    expect(result.loginToken).toBeUndefined();
  });

  it.each([
    ["ordinary token mixed in", recoveryLoginData({ token: "jwt" })],
    ["user mixed in", recoveryLoginData({ user: { id: 1, username: "admin", role: "admin" } })],
    ["pending 2FA mixed in", recoveryLoginData({ requires_2fa: true, login_token: "pending" })],
    ["login_token mixed in", recoveryLoginData({ login_token: "pending" })],
    ["flag false", recoveryLoginData({ requires_reenrollment: false })],
    ["flag truthy string", recoveryLoginData({ requires_reenrollment: "true" })],
    ["missing token", (({ recovery_token: _omit, ...rest }) => rest)(recoveryLoginData())],
    ["empty token", recoveryLoginData({ recovery_token: "" })],
    ["token with whitespace", recoveryLoginData({ recovery_token: "aaa.bbb ccc" })],
    ["missing enrollment", (({ enrollment: _omit, ...rest }) => rest)(recoveryLoginData())],
    ["array enrollment", recoveryLoginData({ enrollment: [] })],
    ["unknown enrollment field", recoveryLoginData({}, { extra: "x" })],
    ["empty secret", recoveryLoginData({}, { secret: "" })],
    ["non base32 secret", recoveryLoginData({}, { secret: "not-base32!" })],
    ["http qr url", recoveryLoginData({}, { qr_url: "https://evil.example/qr" })],
    ["hotp qr url", recoveryLoginData({}, { qr_url: RECOVERY_QR.replace("otpauth://totp/", "otpauth://hotp/") })],
    ["qr secret mismatch", recoveryLoginData({}, { qr_url: RECOVERY_QR.replace(RECOVERY_SECRET, "KRSXG5CTMVRXEZLU") })],
    ["empty issuer", recoveryLoginData({}, { issuer: " " })],
    ["empty enrollment id", recoveryLoginData({}, { enrollment_id: "" })],
    ["non RFC3339 expiry", recoveryLoginData({}, { expires_at: "2026-10-16 12:10:00" })],
    ["impossible date", recoveryLoginData({}, { expires_at: "2026-02-30T12:00:00Z" })],
    ["numeric expiry", recoveryLoginData({}, { expires_at: 1_800_000_000 })],
  ])("rejects malformed or mixed recovery response: %s", async (_name, data) => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, data));

    const captured = await api.login("admin", "secret").catch((error: unknown) => error);

    expect(captured).toBeInstanceOf(ApiError);
    // The secret-bearing payload must not be echoed into the error.
    expect((captured as ApiError).detail).toBeUndefined();
    expect(JSON.stringify(captured)).not.toContain(RECOVERY_SECRET);
  });

  it("keeps the ordinary session branch unchanged", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, {
      token: "jwt",
      user: { id: 3, username: "admin", role: "admin", totp_enabled: true },
    }));

    await expect(api.login("admin", "secret")).resolves.toEqual({
      token: "jwt",
      user: { id: 3, username: "admin", role: "admin", totpEnabled: true },
      requires2FA: false,
      loginToken: undefined,
    });
  });

  it("parses strict RFC3339 instants only", () => {
    expect(parseRFC3339("2026-10-16T04:10:00Z")).toBe(Date.UTC(2026, 9, 16, 4, 10, 0));
    expect(parseRFC3339("2026-10-16T04:10:00-01:30")).toBe(Date.UTC(2026, 9, 16, 5, 40, 0));
    expect(parseRFC3339("2026-10-16T24:00:00Z")).toBeNull();
    expect(parseRFC3339("2026-13-01T00:00:00Z")).toBeNull();
    expect(parseRFC3339("2026-10-16T04:10:00")).toBeNull();
    expect(parseRFC3339("2026-10-16T04:10:00+24:00")).toBeNull();
  });
});

describe("auth-api completeRecoveryEnrollment", () => {
  const fetchMock = vi.fn();
  const api = createAuthApi();
  const originalHref = window.location.href;

  beforeEach(() => {
    sessionStorage.clear();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    sessionStorage.clear();
  });

  it("sends only the explicit recovery Bearer and code, returning exactly this response's codes", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, { recovery_codes: EIGHT_CODES }));

    await expect(api.completeRecoveryEnrollment(RECOVERY_TOKEN, "123456")).resolves.toEqual({ recoveryCodes: EIGHT_CODES });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/auth\/recovery\/enrollment\/verify$/);
    expect(init.method).toBe("POST");
    expect(init.headers).toEqual({ "Content-Type": "application/json", Authorization: `Bearer ${RECOVERY_TOKEN}` });
    expect(init.body).toBe(JSON.stringify({ code: "123456" }));
    expect(sessionStorage.length).toBe(0);
  });

  it.each([
    ["seven codes", { recovery_codes: EIGHT_CODES.slice(0, 7) }],
    ["nine codes", { recovery_codes: [...EIGHT_CODES, "IIII9999"] }],
    ["duplicate code", { recovery_codes: [...EIGHT_CODES.slice(0, 7), EIGHT_CODES[0]] }],
    ["empty code", { recovery_codes: [...EIGHT_CODES.slice(0, 7), ""] }],
    ["non-string code", { recovery_codes: [...EIGHT_CODES.slice(0, 7), 12345678] }],
    ["session token mixed in", { recovery_codes: EIGHT_CODES, token: "jwt" }],
    ["missing list", {}],
  ])("treats a malformed success as unknown: %s", async (_name, data) => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, data));

    await expect(api.completeRecoveryEnrollment(RECOVERY_TOKEN, "123456")).rejects.toMatchObject({ kind: "unknown" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("classifies 401 RECOVERY_ENROLLMENT_INVALID without invalidating the stored ordinary session", async () => {
    sessionStorage.setItem("xirang-auth-token", "unrelated-session");
    const generation = getAuthSessionGeneration();
    fetchMock.mockResolvedValueOnce(envelopeResponse(401, {
      code: 401, message: "恢复绑定凭据无效或已过期", data: { error_code: "RECOVERY_ENROLLMENT_INVALID" },
    }));

    await expect(api.completeRecoveryEnrollment(RECOVERY_TOKEN, "123456")).rejects.toMatchObject({ kind: "enrollment-invalid" });

    expect(sessionStorage.getItem("xirang-auth-token")).toBe("unrelated-session");
    expect(getAuthSessionGeneration()).toBe(generation);
    expect(window.location.href).toBe(originalHref);
  });

  it.each([
    ["403 TOTP_CODE_INVALID", 403, { code: 403, message: "当前验证码错误", data: { error_code: "TOTP_CODE_INVALID" } }, "code-invalid"],
    ["400 format", 400, { code: 400, message: "请求参数不合法", data: null }, "invalid-request"],
    ["429 rate limit", 429, { code: 429, message: "too many", data: null }, "rate-limited"],
    ["500", 500, { code: 500, message: "internal", data: null }, "unknown"],
    ["502", 502, { code: 502, message: "bad gateway", data: null }, "unknown"],
    ["401 without machine code", 401, { code: 401, message: "expired", data: null }, "unknown"],
    ["403 other machine code", 403, { code: 403, message: "x", data: { error_code: "STEP_UP_REQUIRED" } }, "unknown"],
    ["200 envelope error", 200, { code: 401, message: "x", data: { error_code: "RECOVERY_ENROLLMENT_INVALID" } }, "unknown"],
  ])("classifies %s by real HTTP status", async (_name, status, envelope, kind) => {
    fetchMock.mockResolvedValueOnce(envelopeResponse(status, envelope));

    await expect(api.completeRecoveryEnrollment(RECOVERY_TOKEN, "123456")).rejects.toMatchObject({ kind });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("treats a network failure as unknown and never retries", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));

    await expect(api.completeRecoveryEnrollment(RECOVERY_TOKEN, "123456")).rejects.toMatchObject({ kind: "unknown" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("refuses a non-token credential or malformed code before sending", async () => {
    await expect(api.completeRecoveryEnrollment("", "123456")).rejects.toMatchObject({ kind: "enrollment-invalid" });
    await expect(api.completeRecoveryEnrollment(RECOVERY_TOKEN, "12345")).rejects.toMatchObject({ kind: "invalid-request" });
    await expect(api.completeRecoveryEnrollment(RECOVERY_TOKEN, "12345a")).rejects.toMatchObject({ kind: "invalid-request" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("does not trust an envelope status without a real HTTP status", () => {
    const error = new ApiError(403, "x", { data: { error_code: "TOTP_CODE_INVALID" } });
    expect(classifyRecoveryEnrollmentFailure(error).kind).toBe("unknown");
    expect(classifyRecoveryEnrollmentFailure(new RecoveryEnrollmentError("code-invalid")).kind).toBe("code-invalid");
  });
});

describe("subscribeAuthSessionGeneration", () => {
  it("notifies every generation bump until unsubscribed", () => {
    const listener = vi.fn();
    const unsubscribe = subscribeAuthSessionGeneration(listener);

    bumpAuthSessionGeneration();
    bumpAuthSessionGeneration();
    expect(listener).toHaveBeenCalledTimes(2);

    unsubscribe();
    bumpAuthSessionGeneration();
    expect(listener).toHaveBeenCalledTimes(2);
  });
});
