import { useContext } from "react";
import {
  IntegrationsContext,
  type IntegrationsContextValue,
} from "@/context/integrations-context.shared";

export function useIntegrationsContext(): IntegrationsContextValue {
  const ctx = useContext(IntegrationsContext);
  if (!ctx)
    throw new Error("useIntegrationsContext must be used within IntegrationsContextProvider");
  return ctx;
}
