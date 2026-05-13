import { createContext } from "react";
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

export const PoliciesContext = createContext<PoliciesContextValue | null>(null);
