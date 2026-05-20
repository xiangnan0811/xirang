import { useCallback } from "react";
import { useAuth } from "@/context/auth-context.hooks";
import { isStepUpRequiredError } from "@/lib/api/core";

export function useStepUpAction() {
  const { ensureStepUpProof, clearStepUpProof } = useAuth();

  return useCallback(async <T,>(action: (stepUpProof?: string) => Promise<T>): Promise<T> => {
    try {
      return await action();
    } catch (error) {
      if (!isStepUpRequiredError(error)) {
        throw error;
      }
      const proof = await ensureStepUpProof();
      try {
        return await action(proof);
      } catch (retryError) {
        if (isStepUpRequiredError(retryError)) {
          clearStepUpProof();
        }
        throw retryError;
      }
    }
  }, [clearStepUpProof, ensureStepUpProof]);
}
