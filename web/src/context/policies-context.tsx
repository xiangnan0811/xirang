import { createContext, useContext, type ReactNode } from "react";
import type { NewPolicyInput, PolicyRecord } from "@/types/domain";

export interface PoliciesContextValue {
  policies: PolicyRecord[];
  refreshPolicies: () => Promise<void>;
  createPolicy: (input: NewPolicyInput) => Promise<void>;
  updatePolicy: (policyId: number, input: NewPolicyInput) => Promise<void>;
  deletePolicy: (policyId: number) => Promise<void>;
  togglePolicy: (policyId: number) => Promise<void>;
  updatePolicySchedule: (
    policyId: number,
    cron: string,
    naturalLanguage: string
  ) => Promise<void>;
}

const PoliciesContext = createContext<PoliciesContextValue | null>(null);

export function usePoliciesContext(): PoliciesContextValue {
  const ctx = useContext(PoliciesContext);
  if (!ctx) throw new Error("usePoliciesContext must be used within PoliciesContextProvider");
  return ctx;
}

export function PoliciesContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: PoliciesContextValue;
}) {
  return <PoliciesContext.Provider value={value}>{children}</PoliciesContext.Provider>;
}
