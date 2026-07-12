import { describe, expect, it } from "vitest";
import { mapAppCredential, mapProfileSchema } from "./credentials";

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
