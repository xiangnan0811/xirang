import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAlertDeliveriesApi } from "./alert-deliveries";

function createMockResponse(status = 200, body = "{}") {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

describe("alert-deliveries API numeric path contract", () => {
  const fetchMock = vi.fn();
  const api = createAlertDeliveriesApi();

  beforeEach(() => {
    fetchMock.mockResolvedValue(createMockResponse(200, "{}"));
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("converts prefixed delivery id to numeric endpoint path", async () => {
    await api.retryDelivery("test-token", "delivery-42");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];

    expect(url).toBe("/api/v1/alert-deliveries/42/retry");
    expect(init.method).toBe("POST");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer test-token",
    });
  });

  it("throws on non-numeric invalid delivery id format", async () => {
    await expect(api.retryDelivery("test-token", "delivery-invalid")).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
