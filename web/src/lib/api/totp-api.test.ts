import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTOTPApi, STEP_UP_ACTIONS } from "./totp-api";

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
    await expect(api.totpVerify("token-1", "123456", "  ")).rejects.toThrow("enrollment_id is required");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("verify 请求 JSON 包含 code 和 enrollment_id", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      recovery_codes: ["aaaa-bbbb"],
    }));
    const api = createTOTPApi();
    await expect(api.totpVerify("token-1", "123456", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")).resolves.toEqual({
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
});
