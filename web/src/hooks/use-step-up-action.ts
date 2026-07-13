import { useCallback } from "react";
import { useAuth } from "@/context/auth-context.hooks";
import type { StepUpProofOptions } from "@/context/auth-context.shared";
import { isStepUpRequiredError } from "@/lib/api/core";
import type { StepUpAction } from "@/lib/api/totp-api";

export function useStepUpAction(stepUpAction: StepUpAction, options?: StepUpProofOptions) {
  const { ensureStepUpProof, clearStepUpProof } = useAuth();
  const hasOptions = options?.persist !== undefined || options?.reuseCached !== undefined;
  const persist = options?.persist;
  const reuseCached = options?.reuseCached;

  return useCallback(async <T,>(action: (stepUpProof?: string) => Promise<T>): Promise<T> => {
    try {
      return await action();
    } catch (error) {
      if (!isStepUpRequiredError(error)) {
        throw error;
      }
      const proof = hasOptions
        ? await ensureStepUpProof(stepUpAction, { persist, reuseCached })
        : await ensureStepUpProof(stepUpAction);
      try {
        return await action(proof);
      } catch (retryError) {
        if (isStepUpRequiredError(retryError)) {
          clearStepUpProof(stepUpAction);
        }
        throw retryError;
      }
    }
  }, [clearStepUpProof, ensureStepUpProof, hasOptions, persist, reuseCached, stepUpAction]);
}
