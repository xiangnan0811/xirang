import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPoliciesApi } from "./policies-api";

function createMockResponse(body: unknown) {
  return {
    status: 200,
    ok: true,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify({ code: 0, message: "ok", data: body })),
  } as unknown as Response;
}

describe("policies api mapping", () => {
  const fetchMock = vi.fn();
  const api = createPoliciesApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps drill-trigger task_run_id to taskRunId", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      task_run_id: "42",
      message: "恢复演练已触发",
    }));

    await expect(api.triggerDrill("token", 7)).resolves.toEqual({
      taskRunId: 42,
      message: "恢复演练已触发",
    });
    expect(String(fetchMock.mock.calls[0][0])).toBe("/api/v1/policies/7/drill-trigger");
    expect((fetchMock.mock.calls[0][1] as RequestInit).method).toBe("POST");
  });
});
