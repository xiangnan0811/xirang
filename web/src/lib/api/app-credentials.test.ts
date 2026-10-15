import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const token = "FAKE_APP_CREDENTIAL_TOKEN_FOR_TEST_ONLY";
const methods = ["getCredentials", "getProfiles"] as const;
const credential = { id: 7, name: "Production", type: "mysql", has_password: true };
const profile = { id: "mysql", name: "MySQL", credential_type: "mysql" };

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
  sessionStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async (input: string) => new Response(JSON.stringify({
    code: 200,
    data: input.endsWith("/profiles") ? [profile] : [credential],
  }), { status: 200 })));
});

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
});

async function setup() {
  // Reload the real module graph to exercise cold import boundaries and isolate auth state.
  const core = await import("./core");
  const { createAppCredentialsApi } = await import("./app-credentials");
  return { core, api: createAppCredentialsApi() };
}

function expected(method: typeof methods[number]) {
  return method === "getCredentials"
    ? { id: 7, type: "mysql", hasPassword: true }
    : { id: "mysql", credentialType: "mysql" };
}

describe.each(methods)("%s loading boundary", (method) => {
  it.each(["before", "during"] as const)("preserves cancellation reason %s loading without dispatching", async (timing) => {
    const { api } = await setup();
    const controller = new AbortController();
    const reason = new Error("dialog closed");
    if (timing === "before") controller.abort(reason);
    const pending = api[method](token, controller.signal);
    if (timing === "during") controller.abort(reason);
    await expect(pending).rejects.toBe(reason);
    expect(fetch).not.toHaveBeenCalled();
    await expect(api[method](token, new AbortController().signal)).resolves.toEqual([
      expect.objectContaining(expected(method)),
    ]);
  });

  it.each([1, 2])("rejects an old operation after %i identity generations even with the same token", async (bumps) => {
    const { api, core } = await setup();
    core.rememberAuthIdentity(token, "admin");
    const pending = api[method](token);
    for (let i = 0; i < bumps; i++) core.bumpAuthSessionGeneration();
    core.rememberAuthIdentity(token, "admin");
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(fetch).not.toHaveBeenCalled();
    await expect(api[method](token)).resolves.toEqual([expect.objectContaining(expected(method))]);
  });

  it("rejects a barrier active at entry even when immediately released", async () => {
    const { api, core } = await setup();
    const barrier = core.beginAuthTransitionBarrier();
    const pending = api[method](token);
    core.releaseAuthTransitionBarrier(barrier);
    await expect(pending).rejects.toBeInstanceOf(core.AuthTransitionRejectedError);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("rejects a barrier activated while the module is loading", async () => {
    const { api, core } = await setup();
    const pending = api[method](token);
    core.beginAuthTransitionBarrier();
    await expect(pending).rejects.toBeInstanceOf(core.AuthTransitionRejectedError);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("preserves the real HTTP error and envelope code", async () => {
    const { api, core } = await setup();
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ code: 503, message: "Unavailable" }), { status: 502 }));
    const pending = api[method](token);
    await expect(pending).rejects.toBeInstanceOf(core.ApiError);
    await expect(pending).rejects.toMatchObject({
      status: 502, httpStatus: 502, message: "Unavailable", detail: { code: 503 },
    });
  });
});

it("maps concurrent reads and still invalidates operations when the module is hot", async () => {
  const { api, core } = await setup();
  const [credentials, profiles] = await Promise.all([api.getCredentials(token), api.getProfiles(token)]);
  expect(credentials).toEqual([expect.objectContaining(expected("getCredentials"))]);
  expect(profiles).toEqual([expect.objectContaining(expected("getProfiles"))]);
  vi.mocked(fetch).mockClear();
  const controller = new AbortController();
  const cancelled = api.getCredentials(token, controller.signal);
  const stale = api.getProfiles(token);
  controller.abort();
  core.bumpAuthSessionGeneration();
  await expect(cancelled).rejects.toMatchObject({ name: "AbortError" });
  await expect(stale).rejects.toMatchObject({ name: "AbortError" });
  expect(fetch).not.toHaveBeenCalled();
});
