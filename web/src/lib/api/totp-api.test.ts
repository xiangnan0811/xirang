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
});
