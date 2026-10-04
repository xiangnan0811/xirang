import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAlertsApi, mapAlertDelivery, mapAlertDeliveryStatus } from "./alerts-api";

describe("alerts-api delivery status mapping", () => {
  it("preserves exact known delivery states", () => {
    expect(mapAlertDeliveryStatus("pending")).toBe("pending");
    expect(mapAlertDeliveryStatus("sending")).toBe("sending");
    expect(mapAlertDeliveryStatus("retrying")).toBe("retrying");
    expect(mapAlertDeliveryStatus("sent")).toBe("sent");
    expect(mapAlertDeliveryStatus("failed")).toBe("failed");
  });

  it("never maps pending to sent", () => {
    const status = mapAlertDeliveryStatus("pending");
    expect(status).not.toBe("sent");
    expect(status).toBe("pending");

    const record = mapAlertDelivery({
      id: 101,
      alert_id: 42,
      integration_id: 3,
      status: "pending",
      created_at: "2026-09-10T10:00:00Z",
    });
    expect(record.status).not.toBe("sent");
    expect(record.status).toBe("pending");
  });

  it("never maps unknown status to sent", () => {
    expect(mapAlertDeliveryStatus(null)).toBe("unknown");
    expect(mapAlertDeliveryStatus(undefined)).toBe("unknown");
    expect(mapAlertDeliveryStatus("")).toBe("unknown");
    expect(mapAlertDeliveryStatus("unexpected_future_status")).toBe("unknown");

    const record = mapAlertDelivery({
      id: 102,
      alert_id: 42,
      integration_id: 3,
      status: "unexpected_status",
      created_at: "2026-09-10T10:00:00Z",
    });
    expect(record.status).not.toBe("sent");
    expect(record.status).toBe("unknown");
  });

  it("maps fields correctly for retrying and failed delivery records", () => {
    const retryingRecord = mapAlertDelivery({
      id: 103,
      alert_id: 42,
      integration_id: 5,
      status: "retrying",
      created_at: "2026-09-10T10:00:00Z",
      attempt_count: 2,
      next_retry_at: "2026-09-10T10:05:00Z",
      last_error: "connection timeout",
    });
    expect(retryingRecord).toMatchObject({
      id: "delivery-103",
      alertId: "alert-42",
      integrationId: "int-5",
      status: "retrying",
      attemptCount: 2,
      nextRetryAt: "2026-09-10T10:05:00Z",
      lastError: "connection timeout",
    });

    const failedRecord = mapAlertDelivery({
      id: 104,
      alert_id: 42,
      integration_id: 5,
      status: "failed",
      created_at: "2026-09-10T10:00:00Z",
      attempt_count: 4,
      last_error: "max retries exceeded",
    });
    expect(failedRecord).toMatchObject({
      id: "delivery-104",
      alertId: "alert-42",
      integrationId: "int-5",
      status: "failed",
      attemptCount: 4,
      lastError: "max retries exceeded",
    });
  });
});


function unreadCountResponse(data: unknown, status = 200) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify({
      code: status === 200 ? 0 : status,
      message: status === 200 ? "ok" : "unavailable",
      data,
    })),
  } as unknown as Response;
}

describe("getAlertUnreadCount", () => {
  const fetchMock = vi.fn();
  const api = createAlertsApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps a valid all-zero unread count as success", async () => {
    fetchMock.mockResolvedValueOnce(unreadCountResponse({ total: 0, critical: 0, warning: 0 }));

    await expect(api.getAlertUnreadCount("token")).resolves.toEqual({
      total: 0,
      critical: 0,
      warning: 0,
    });
  });

  it("rejects HTTP 500 instead of inventing a zero success", async () => {
    fetchMock.mockResolvedValueOnce(unreadCountResponse({ total: 1, critical: 1, warning: 0 }, 500));

    await expect(api.getAlertUnreadCount("token")).rejects.toMatchObject({ status: 500 });
  });

  it("rejects when a pending unread request is aborted", async () => {
    const controller = new AbortController();
    fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
      let rejectPromise: (error: unknown) => void = () => {};
      const promise = new Promise<Response>((_resolve, reject) => {
        rejectPromise = reject;
      });
      const fail = () => {
        rejectPromise(Object.assign(new Error("The operation was aborted"), { name: "AbortError" }));
      };
      if (init?.signal?.aborted) {
        fail();
        return promise;
      }
      init?.signal?.addEventListener("abort", fail, { once: true });
      return promise;
    });

    const pending = api.getAlertUnreadCount("token", { signal: controller.signal });
    controller.abort();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });
});
