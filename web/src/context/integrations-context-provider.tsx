import type { ReactNode } from "react";
import {
  IntegrationsContext,
  type IntegrationsContextValue,
} from "@/context/integrations-context.shared";

export function IntegrationsContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: IntegrationsContextValue;
}) {
  return <IntegrationsContext.Provider value={value}>{children}</IntegrationsContext.Provider>;
}
