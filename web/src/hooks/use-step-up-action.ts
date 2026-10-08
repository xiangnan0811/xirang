import { useCallback } from "react";
import { useAuth } from "@/context/auth-context.hooks";
import type { StepUpProofOptions } from "@/context/auth-context.shared";
import { getAuthSessionGeneration, isAuthTransitionActive, isStepUpRequiredError } from "@/lib/api/core";
import {
  assertSensitiveStepUpCurrent,
  assertSensitiveStepUpReady,
} from "@/lib/sensitive-step-up";
import type { StepUpAction } from "@/lib/api/totp-api";

export function useStepUpAction(stepUpAction: StepUpAction, options?: StepUpProofOptions) {
  const {
    ensureStepUpProof,
    clearStepUpProof,
    token,
    totpEnabled,
  } = useAuth();
  const hasOptions = options?.persist !== undefined || options?.reuseCached !== undefined;
  const persist = options?.persist;
  const reuseCached = options?.reuseCached;
  const holdsCachedProof = (reuseCached ?? persist ?? true) === true;

  return useCallback(async <T,>(action: (stepUpProof?: string) => Promise<T>): Promise<T> => {
    const generation = assertSensitiveStepUpReady({
      token,
      totpEnabled,
      holdsCachedProof,
    });
    try {
      const result = await action();
      assertSensitiveStepUpCurrent(generation, holdsCachedProof);
      return result;
    } catch (error) {
      if (!isStepUpRequiredError(error)) {
        throw error;
      }
      assertSensitiveStepUpCurrent(generation, holdsCachedProof);
      const proof = hasOptions
        ? await ensureStepUpProof(stepUpAction, { persist, reuseCached })
        : await ensureStepUpProof(stepUpAction);
      assertSensitiveStepUpCurrent(generation, holdsCachedProof);
      try {
        const retried = await action(proof);
        assertSensitiveStepUpCurrent(generation, holdsCachedProof);
        return retried;
      } catch (retryError) {
        if (
          isStepUpRequiredError(retryError)
          && getAuthSessionGeneration() === generation
          && !isAuthTransitionActive()
        ) {
          clearStepUpProof(stepUpAction);
        }
        throw retryError;
      }
    }
  }, [
    clearStepUpProof,
    ensureStepUpProof,
    hasOptions,
    holdsCachedProof,
    persist,
    reuseCached,
    stepUpAction,
    token,
    totpEnabled,
  ]);
}
