import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { ApiError, request } from "./core";
import type * as Core from "./core";
import { createCredentialsApi, mapAppCredential, mapProfileSchema } from "./credentials";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof Core>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);

describe("credentials mappers", () => {
  it("maps AppCredential wire fields to camelCase", () => {
    expect(
      mapAppCredential({
        id: 1,
        name: "db",
        type: "mysql",
        description: "prod",
        config: { host: "h" },
        has_password: true,
        reference_count: 3,
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-02T00:00:00Z",
      }),
    ).toEqual({
      id: 1,
      name: "db",
      type: "mysql",
      description: "prod",
      config: { host: "h" },
      hasPassword: true,
      referenceCount: 3,
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-02T00:00:00Z",
    });
  });

  it("maps ProfileSchema wire fields to camelCase", () => {
    expect(
      mapProfileSchema({
        id: "mysql",
        name: "MySQL",
        description: "d",
        credential_type: "mysql",
        is_docker: false,
        config_schema: [{ key: "host", label: "Host", type: "text", required: true }],
      }),
    ).toEqual({
      id: "mysql",
      name: "MySQL",
      description: "d",
      credentialType: "mysql",
      isDocker: false,
      configSchema: [{ key: "host", label: "Host", type: "text", required: true }],
    });
  });

  it("normalizes malformed credential/profile payloads", () => {
    expect(
      mapAppCredential({
        id: "7",
        name: null,
        config: { port: 3306, skip: null } as unknown as Record<string, string>,
        reference_count: "2",
      } as never),
    ).toMatchObject({
      id: 7,
      name: "",
      config: { port: "3306" },
      referenceCount: 2,
    });

    expect(
      mapProfileSchema({
        id: 1,
        config_schema: [{ key: "host" }, { label: "no-key" }, "bad", null],
      } as never),
    ).toMatchObject({
      id: "1",
      configSchema: [{ key: "host", label: "host", type: "text", required: false }],
    });
  });
});

describe("credential references API", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("maps a paginated response to the id/name-only projection and sends ascending ID paging", async () => {
    const signal = new AbortController().signal;
    requestMock.mockResolvedValueOnce({
      code: 0,
      message: "ok",
      data: [
        {
          id: 12,
          name: "Policy 12",
          app_credential_id: 7,
          config: "SECRET_CONFIG_SENTINEL",
        },
        {
          id: 13,
          name: "Policy 13",
          password: "SECRET_PASSWORD_SENTINEL",
        },
      ],
      total: 45,
      page: 2,
      page_size: 20,
    });

    const result = await createCredentialsApi().listReferences(
      "auth-marker",
      7,
      { page: 2, pageSize: 20 },
      signal,
    );

    expect(requestMock).toHaveBeenCalledWith(
      "/app-credentials/7/references?page=2&page_size=20&sort_by=id&sort_order=asc",
      { token: "auth-marker", signal },
    );
    expect(result).toEqual({
      items: [
        { id: 12, name: "Policy 12" },
        { id: 13, name: "Policy 13" },
      ],
      total: 45,
      page: 2,
      pageSize: 20,
    });
    expect(JSON.stringify(result)).not.toContain("SECRET_");
    expect(result.items[0]).not.toHaveProperty("app_credential_id");
  });

  it("accepts an empty page and the inclusive safe pagination boundaries", async () => {
    requestMock.mockResolvedValueOnce({
      code: 0,
      message: "ok",
      data: [],
      total: 0,
      page: 1,
      page_size: 500,
    });

    await expect(
      createCredentialsApi().listReferences("auth-marker", 7, { page: 1, pageSize: 500 }),
    ).resolves.toEqual({
      items: [],
      total: 0,
      page: 1,
      pageSize: 500,
    });

    requestMock.mockResolvedValueOnce({
      code: 0,
      message: "ok",
      data: [{ id: Number.MAX_SAFE_INTEGER, name: "Largest safe ID" }],
      total: Number.MAX_SAFE_INTEGER,
      page: Number.MAX_SAFE_INTEGER,
      page_size: 500,
    });

    await expect(
      createCredentialsApi().listReferences(
        "auth-marker",
        7,
        { page: Number.MAX_SAFE_INTEGER, pageSize: 500 },
      ),
    ).resolves.toEqual({
      items: [{ id: Number.MAX_SAFE_INTEGER, name: "Largest safe ID" }],
      total: Number.MAX_SAFE_INTEGER,
      page: Number.MAX_SAFE_INTEGER,
      pageSize: 500,
    });
  });

  it.each([
    ["missing envelope code", { code: undefined }],
    ["missing envelope message", { message: undefined }],
    ["non-array data", { data: null }],
    ["object data", { data: {} }],
    ["null reference row", { data: [null] }],
    ["zero reference ID", { data: [{ id: 0, name: "Policy" }] }],
    ["negative reference ID", { data: [{ id: -1, name: "Policy" }] }],
    ["fractional reference ID", { data: [{ id: 1.5, name: "Policy" }] }],
    ["unsafe reference ID", { data: [{ id: Number.MAX_SAFE_INTEGER + 1, name: "Policy" }] }],
    ["string reference ID", { data: [{ id: "1", name: "Policy" }] }],
    ["non-string reference name", { data: [{ id: 1, name: null }] }],
    ["negative total", { total: -1 }],
    ["fractional total", { total: 1.5 }],
    ["unsafe total", { total: Number.MAX_SAFE_INTEGER + 1 }],
    ["string total", { total: "1" }],
    ["zero page", { page: 0 }],
    ["negative page", { page: -1 }],
    ["fractional page", { page: 1.5 }],
    ["unsafe page", { page: Number.MAX_SAFE_INTEGER + 1 }],
    ["string page", { page: "1" }],
    ["zero page size", { page_size: 0 }],
    ["page size above limit", { page_size: 501 }],
    ["fractional page size", { page_size: 20.5 }],
    ["unsafe page size", { page_size: Number.MAX_SAFE_INTEGER + 1 }],
    ["string page size", { page_size: "20" }],
  ] as Array<[string, Record<string, unknown>]>)(
    "rejects %s instead of treating malformed references as an empty page",
    async (_label, overrides) => {
      requestMock.mockResolvedValueOnce({
        code: 0,
        message: "ok",
        data: [{ id: 1, name: "Policy", config: "SECRET_CONFIG_SENTINEL" }],
        total: 1,
        page: 1,
        page_size: 20,
        ...overrides,
      });

      await expect(
        createCredentialsApi().listReferences("auth-marker", 7, { page: 1, pageSize: 20 }),
      ).rejects.toMatchObject({
        status: 500,
        message: i18n.t("credentials.referencesInvalidResponse"),
      });
      expect(requestMock).toHaveBeenCalledTimes(1);
    },
  );

  it("forwards an AbortSignal and preserves an abort rejection from the request boundary", async () => {
    const controller = new AbortController();
    const abortError = Object.assign(new Error("aborted"), { name: "AbortError" });
    requestMock.mockRejectedValueOnce(abortError);

    const pending = createCredentialsApi().listReferences(
      "auth-marker",
      7,
      { page: 2, pageSize: 20 },
      controller.signal,
    );
    controller.abort();

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(requestMock).toHaveBeenCalledWith(
      "/app-credentials/7/references?page=2&page_size=20&sort_by=id&sort_order=asc",
      { token: "auth-marker", signal: controller.signal },
    );
  });

  it("raises a localized safe ApiError without exposing malformed payload data", async () => {
    requestMock.mockResolvedValueOnce({
      code: 0,
      message: "ok",
      data: [{ id: 0, name: "Policy", secret: "SECRET_PAYLOAD_SENTINEL" }],
      total: 1,
      page: 1,
      page_size: 20,
    });

    const error = await createCredentialsApi()
      .listReferences("auth-marker", 7, { page: 1, pageSize: 20 })
      .catch((value: unknown) => value);

    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      status: 500,
      message: i18n.t("credentials.referencesInvalidResponse"),
    });
    expect(String(error)).not.toContain("SECRET_PAYLOAD_SENTINEL");
  });
});
