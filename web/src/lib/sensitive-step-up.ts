import i18n from "@/i18n";
import {
  AuthTransitionRejectedError,
  getAuthSessionGeneration,
  isAuthTransitionActive,
} from "@/lib/api/core";
import { assertStepUpPrerequisite, StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";

export class StepUpPausedError extends Error {
  readonly code = "STEP_UP_PAUSED";

  constructor() {
    super(i18n.t("stepUp.authTransitioning"));
    this.name = "StepUpPausedError";
  }
}

function rejectActiveAuthTransition(holdsCachedProof: boolean): void {
  if (!isAuthTransitionActive()) return;
  if (holdsCachedProof) throw new AuthTransitionRejectedError();
  throw new StepUpPausedError();
}

export function assertSensitiveStepUpReady(input: {
  token: string | null;
  totpEnabled: boolean;
  holdsCachedProof?: boolean;
}): number {
  rejectActiveAuthTransition(input.holdsCachedProof === true);
  assertStepUpPrerequisite(input.token, input.totpEnabled);
  return getAuthSessionGeneration();
}

export function assertSensitiveStepUpCurrent(generation: number, holdsCachedProof = false): void {
  rejectActiveAuthTransition(holdsCachedProof);
  if (getAuthSessionGeneration() !== generation) {
    throw new StepUpPausedError();
  }
}

export function sensitiveStepUpBlock(input: {
  token: string | null;
  totpEnabled: boolean;
}): "ready" | "prerequisite" | "paused" {
  try {
    assertSensitiveStepUpReady(input);
    return "ready";
  } catch (error) {
    if (error instanceof StepUpPrerequisiteError) return "prerequisite";
    if (error instanceof StepUpPausedError || error instanceof AuthTransitionRejectedError) return "paused";
    throw error;
  }
}
