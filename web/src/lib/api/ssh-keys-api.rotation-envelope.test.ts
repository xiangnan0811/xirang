import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./core";
import { rotateSSHKey, SSHKeyRotationDecodeError } from "./ssh-keys-api";

const canonicalFingerprint = "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y";

function jsonResponse(status: number, body: unknown) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify(body)),
  } as unknown as Response;
}

describe("rotateSSHKey envelope provenance", () => {
  const fetchMock = vi.fn();

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("decodes a canonical fingerprint from a real HTTP 200 envelope", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, {
      code: 0,
      message: "ok",
      data: {
        status: "saved",
        reason: "",
        public_key_fingerprint: canonicalFingerprint,
        results: [],
      },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(rotateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-4", {
      privateKey: "SECRET",
    })).resolves.toMatchObject({
      status: "saved",
      publicKeyFingerprint: canonicalFingerprint,
    });
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v1/ssh-keys/4/rotate");
  });

  it.each([
    ["null code", { code: null, message: "bad", data: null }],
    ["string code", { code: "400", message: "bad", data: null }],
    ["mismatched code", { code: 400, message: "bad", data: { status: "saved" } }],
  ])("treats HTTP 200 with %s as an unusable rotation response", async (label, body) => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, body));
    vi.stubGlobal("fetch", fetchMock);

    await expect(rotateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-4", {
      privateKey: "SECRET",
    })).rejects.toBeInstanceOf(SSHKeyRotationDecodeError);
    expect(label).not.toBe("");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("keeps a real HTTP 400 rejection definite", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(400, {
      code: 400,
      message: "nope",
      data: null,
    }));
    vi.stubGlobal("fetch", fetchMock);

    const failure = rotateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-4", {
      privateKey: "SECRET",
    });
    await expect(failure).rejects.toBeInstanceOf(ApiError);
    await expect(failure).rejects.toMatchObject({
      status: 400,
      httpStatus: 400,
    });
  });
});
