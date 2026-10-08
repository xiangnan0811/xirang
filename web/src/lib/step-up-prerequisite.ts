import i18n from "@/i18n";

export class StepUpPrerequisiteError extends Error {
  readonly code = "TOTP_REQUIRED";

  constructor() {
    super(i18n.t("stepUp.totpRequired"));
    this.name = "StepUpPrerequisiteError";
  }
}

export function assertStepUpPrerequisite(token: string | null, totpEnabled: boolean): void {
  if (!token) throw new Error(i18n.t("stepUp.loginRequired"));
  if (!totpEnabled) throw new StepUpPrerequisiteError();
}

const SECURITY_RETURN_PATHS: Record<string, true> = {
  "/app/nodes": true,
  "/app/tasks": true,
  "/app/ssh-keys": true,
  "/app/notifications": true,
  "/app/backups/recovery": true,
  "/app/backups/data": true,
  "/app/settings": true,
};

export function securityReturnPath(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const pathname = value.split(/[?#]/, 1)[0];
  return Object.hasOwn(SECURITY_RETURN_PATHS, pathname) ? pathname : undefined;
}
