import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAuthApi } from "./auth-api";

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
