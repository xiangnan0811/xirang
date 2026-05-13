import { createContext } from "react";
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

export const IntegrationsContext = createContext<IntegrationsContextValue | null>(null);
