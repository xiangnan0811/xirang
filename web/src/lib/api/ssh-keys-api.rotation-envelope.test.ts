import { afterEach, describe, expect, it, vi, type Mock } from "vitest";
import { ApiError } from "./core";
import { rotateSSHKey, SSHKeyRotationDecodeError } from "./ssh-key-rotation-api";
import { createSSHKeysApi, SSHKeyUpdateDecodeError } from "./ssh-keys-api";
import { runSSHKeyReplacement, type SSHKeyReplacementOptions } from "@/lib/ssh-key-replacement";
import type { NewSSHKeyInput } from "@/types/domain";

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

type SentRequest = {
  url: string;
  init: RequestInit;
  headers: Record<string, string>;
  body: Record<string, unknown>;
};

describe("SSH key replacement over the real request transport", () => {
  const fetchMock = vi.fn();
  const challengeBody = {
    code: 403,
    message: "step-up required",
    data: { error_code: "STEP_UP_REQUIRED", ttl_seconds: 300 },
  };
  const updateInput: NewSSHKeyInput = {
    name: "ops-key",
    username: "deploy",
    keyType: "ed25519",
    privateKey: "  FAKE_PRIVATE_KEY_FOR_TEST_ONLY  ",
    disabled: false,
    allowedPurposes: "terminal",
    allowedNodeIds: "1",
    allowedNodeTags: "prod",
  };
  const updatedRow = {
    id: 21,
    name: "ops-key",
    username: "deploy",
    key_type: "ed25519",
    fingerprint: "SHA256:test",
    created_at: "2026-05-18T00:00:00Z",
  };

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  function ownedOptions(): { options: SSHKeyReplacementOptions; requestProof: Mock<() => Promise<string>> } {
    const requestProof = vi.fn(async () => "FAKE_FRESH_PROOF_FOR_TEST_ONLY");
    return {
      requestProof,
      options: { signal: new AbortController().signal, isCurrent: () => true, requestProof },
    };
  }

  function sentRequest(index: number): SentRequest {
    const [url, init] = fetchMock.mock.calls[index] as [string, RequestInit];
    return {
      url: String(url),
      init,
      headers: init.headers as Record<string, string>,
      body: JSON.parse(String(init.body)) as Record<string, unknown>,
    };
  }

  it("answers a real HTTP 403 update challenge with one header-only proof resubmission", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(403, challengeBody))
      .mockResolvedValueOnce(jsonResponse(200, { code: 0, message: "ok", data: updatedRow }));
    vi.stubGlobal("fetch", fetchMock);
    const { options, requestProof } = ownedOptions();
    const api = createSSHKeysApi();

    const updated = await runSSHKeyReplacement(
      (proof) => api.updateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-21", updateInput, { stepUpProof: proof, signal: options.signal }),
      options,
    );

    expect(updated).toMatchObject({ id: "key-21", name: "ops-key", fingerprint: "SHA256:test" });
    expect(requestProof).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const first = sentRequest(0);
    const second = sentRequest(1);
    for (const sent of [first, second]) {
      expect(sent.url).toBe("/api/v1/ssh-keys/21");
      expect(sent.init.method).toBe("PUT");
      expect(sent.init.signal).toBe(options.signal);
      expect(sent.body.private_key).toBe("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
      expect(JSON.stringify(sent.body)).not.toContain("FAKE_FRESH_PROOF_FOR_TEST_ONLY");
    }
    expect(first.headers).not.toHaveProperty("X-Xirang-Step-Up");
    expect(second.headers["X-Xirang-Step-Up"]).toBe("FAKE_FRESH_PROOF_FOR_TEST_ONLY");
  });

  it("never replays an update whose HTTP 200 envelope carries a step-up code", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, challengeBody));
    vi.stubGlobal("fetch", fetchMock);
    const { options, requestProof } = ownedOptions();
    const api = createSSHKeysApi();

    const failure = await runSSHKeyReplacement(
      (proof) => api.updateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-21", updateInput, { stepUpProof: proof, signal: options.signal }),
      options,
    ).catch((error: unknown) => error);

    expect(failure).toBeInstanceOf(SSHKeyUpdateDecodeError);
    expect(failure).not.toBeInstanceOf(ApiError);
    expect(String((failure as Error).message)).not.toContain("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(requestProof).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("returns a second real update challenge without a third request", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(403, challengeBody))
      .mockResolvedValueOnce(jsonResponse(403, challengeBody));
    vi.stubGlobal("fetch", fetchMock);
    const { options, requestProof } = ownedOptions();
    const api = createSSHKeysApi();

    await expect(runSSHKeyReplacement(
      (proof) => api.updateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-21", updateInput, { stepUpProof: proof, signal: options.signal }),
      options,
    )).rejects.toMatchObject({ status: 403, httpStatus: 403 });
    expect(requestProof).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("answers a real HTTP 403 rotation challenge with one header-only proof resubmission", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(403, challengeBody))
      .mockResolvedValueOnce(jsonResponse(200, {
        code: 0,
        message: "ok",
        data: { status: "saved", reason: "", public_key_fingerprint: canonicalFingerprint, results: [] },
      }));
    vi.stubGlobal("fetch", fetchMock);
    const { options, requestProof } = ownedOptions();

    await expect(runSSHKeyReplacement(
      (proof) => rotateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-4", { privateKey: "SECRET" }, {
        stepUpProof: proof,
        signal: options.signal,
      }),
      options,
    )).resolves.toMatchObject({ status: "saved", publicKeyFingerprint: canonicalFingerprint });

    expect(requestProof).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const first = sentRequest(0);
    const second = sentRequest(1);
    expect(second.url).toBe("/api/v1/ssh-keys/4/rotate");
    expect(second.init.signal).toBe(options.signal);
    expect(first.headers).not.toHaveProperty("X-Xirang-Step-Up");
    expect(second.headers["X-Xirang-Step-Up"]).toBe("FAKE_FRESH_PROOF_FOR_TEST_ONLY");
    expect(second.body).toEqual({ private_key: "SECRET" });
  });
});
