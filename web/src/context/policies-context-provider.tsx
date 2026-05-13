import type { ReactNode } from "react";
import {
  PoliciesContext,
  type PoliciesContextValue,
} from "@/context/policies-context.shared";

export function PoliciesContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: PoliciesContextValue;
}) {
  return <PoliciesContext.Provider value={value}>{children}</PoliciesContext.Provider>;
}
