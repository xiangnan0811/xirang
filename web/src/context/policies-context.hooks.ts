import { useContext } from "react";
import { PoliciesContext, type PoliciesContextValue } from "@/context/policies-context.shared";

export function usePoliciesContext(): PoliciesContextValue {
  const ctx = useContext(PoliciesContext);
  if (!ctx) throw new Error("usePoliciesContext must be used within PoliciesContextProvider");
  return ctx;
}
