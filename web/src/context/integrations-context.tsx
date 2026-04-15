import { createContext, useContext, type ReactNode } from "react";
import type {
  IntegrationChannel,
  IntegrationProbeResult,
  NewIntegrationInput,
} from "@/types/domain";

export interface IntegrationsContextValue {
  integrations: IntegrationChannel[];
  refreshIntegrations: () => Promise<void>;
  addIntegration: (input: NewIntegrationInput) => Promise<void>;
  removeIntegration: (integrationId: string) => Promise<void>;
  toggleIntegration: (integrationId: string) => Promise<void>;
  updateIntegration: (
    integrationId: string,
    patch: Partial<IntegrationChannel> & { secret?: string; skipEndpointHint?: boolean }
  ) => Promise<void>;
  patchIntegration: (
    integrationId: string,
    patch: Record<string, unknown>
  ) => Promise<void>;
  testIntegration: (integrationId: string) => Promise<IntegrationProbeResult>;
}

const IntegrationsContext = createContext<IntegrationsContextValue | null>(null);

export function useIntegrationsContext(): IntegrationsContextValue {
  const ctx = useContext(IntegrationsContext);
  if (!ctx)
    throw new Error("useIntegrationsContext must be used within IntegrationsContextProvider");
  return ctx;
}

export function IntegrationsContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: IntegrationsContextValue;
}) {
  return <IntegrationsContext.Provider value={value}>{children}</IntegrationsContext.Provider>;
}
