import { getAuthSessionGeneration } from "@/lib/api/core";
import {
  assertSensitiveStepUpCurrent,
  assertSensitiveStepUpReady,
  sensitiveStepUpBlock,
  StepUpPausedError,
} from "@/lib/sensitive-step-up";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";

export interface BackupSensitiveRuntime {
  token: string | null;
  totpEnabled: boolean;
  authTransitioning: boolean;
}

export type BackupSensitiveDenial = "totp_required" | "auth_transitioning";

export function backupSensitiveRuntime(
  token: string | null,
  totpEnabled: boolean | undefined,
  authTransitioning: boolean | undefined,
): BackupSensitiveRuntime {
  return {
    token,
    totpEnabled: totpEnabled === true,
    authTransitioning: authTransitioning === true,
  };
}

export function backupSensitiveBlock(runtime: BackupSensitiveRuntime): BackupSensitiveDenial | null {
  const block = sensitiveStepUpBlock(runtime);
  if (block === "ready") return null;
  return block === "prerequisite" ? "totp_required" : "auth_transitioning";
}

export function beginBackupSensitiveAction(runtime: BackupSensitiveRuntime): number {
  return assertSensitiveStepUpReady(runtime);
}

export function backupSensitiveCurrent(generation: number): boolean {
  try {
    assertSensitiveStepUpCurrent(generation);
    return true;
  } catch (error) {
    if (error instanceof StepUpPausedError) return false;
    throw error;
  }
}

export function backupSensitiveDenialFromError(error: unknown): BackupSensitiveDenial | null {
  if (error instanceof StepUpPrerequisiteError) return "totp_required";
  if (error instanceof StepUpPausedError) return "auth_transitioning";
  return null;
}

export function backupStepUpErrorKey(error: string | null | undefined): "stepUp.totpRequired" | "stepUp.authTransitioning" | null {
  if (error === "totp_required") return "stepUp.totpRequired";
  if (error === "auth_transitioning") return "stepUp.authTransitioning";
  return null;
}

export function backupAuthGeneration(): number {
  return getAuthSessionGeneration();
}
