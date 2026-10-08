import { describe, expect, it } from "vitest";
import { assertStepUpPrerequisite, securityReturnPath, StepUpPrerequisiteError } from "./step-up-prerequisite";

describe("step-up prerequisite", () => {
  it("rejects a disabled factor with a distinct local error", () => {
    try {
      assertStepUpPrerequisite("session", false);
      throw new Error("Expected rejection");
    } catch (error) {
      expect(error).toBeInstanceOf(StepUpPrerequisiteError);
      expect(error).toHaveProperty("code", "TOTP_REQUIRED");
    }
  });

  it("keeps missing authentication distinct from missing TOTP", () => {
    expect(() => assertStepUpPrerequisite(null, true)).toThrow();
    try {
      assertStepUpPrerequisite(null, false);
    } catch (error) {
      expect(error).not.toBeInstanceOf(StepUpPrerequisiteError);
    }
  });
});

describe("security return navigation", () => {
  it.each([
    "/app/nodes", "/app/tasks", "/app/ssh-keys", "/app/notifications",
    "/app/backups/recovery", "/app/backups/data", "/app/settings",
  ])("strips private query and hash state from %s", (path) => {
    expect(securityReturnPath(`${path}?reason=private#secret`)).toBe(path);
  });

  it.each([undefined, null, {}, 3, "https://example.com/app/tasks", "//example.com", "/app/tasks/../settings", "/app/users", "/app/tasks/", "constructor", "__proto__"])("rejects unapproved destination %s", (path) => {
    expect(securityReturnPath(path)).toBeUndefined();
  });
});
