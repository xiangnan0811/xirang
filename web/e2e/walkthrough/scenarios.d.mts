export type WalkthroughCategory = "route" | "dialog" | "edge_state" | "auth_flow";
export type WalkthroughLanguage = "zh" | "en";
export type WalkthroughTheme = "light" | "dark";
export type WalkthroughRole = "admin" | "anonymous";

export interface WalkthroughViewport {
  width: number;
  height: number;
}

export interface WalkthroughScenario {
  id: string;
  category: WalkthroughCategory;
  path: string;
  language: WalkthroughLanguage;
  viewport: WalkthroughViewport;
  theme: WalkthroughTheme;
  role: WalkthroughRole;
  scenario: string;
}

export const SCENARIOS: readonly WalkthroughScenario[];
