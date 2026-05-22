import { useCallback } from "react";
import { useAuth } from "@/context/auth-context.hooks";
import type { StepUpProofOptions } from "@/context/auth-context.shared";
import { isStepUpRequiredError } from "@/lib/api/core";

export function useStepUpAction(options?: StepUpProofOptions) {
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
        ? await ensureStepUpProof({ persist, reuseCached })
        : await ensureStepUpProof();
      try {
        return await action(proof);
      } catch (retryError) {
        if (isStepUpRequiredError(retryError)) {
          clearStepUpProof();
        }
        throw retryError;
      }
    }
  }, [clearStepUpProof, ensureStepUpProof, hasOptions, persist, reuseCached]);
}
