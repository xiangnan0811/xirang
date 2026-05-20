import { beforeEach, describe, expect, it, vi } from "vitest";
import { createSnapshotsApi } from "./snapshots-api";
import { request } from "./core";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);

describe("snapshots api", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("restoreSnapshot 会附加 step-up proof", async () => {
    requestMock.mockResolvedValueOnce(undefined);

    await createSnapshotsApi().restoreSnapshot("token-1", 101, "abcdef123456", ["/data/a"], "/tmp/restore", "proof-1");

    expect(requestMock).toHaveBeenCalledWith("/tasks/101/snapshots/abcdef123456/restore", {
      method: "POST",
      token: "token-1",
      stepUpProof: "proof-1",
      body: { includes: ["/data/a"], targetPath: "/tmp/restore" },
    });
  });
});
