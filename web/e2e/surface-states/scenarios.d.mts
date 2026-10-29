export type SurfaceCategory = "docker_state" | "docker_role" | "docker_isolation" | "history_state" | "history_isolation";
export type SurfaceLanguage = "zh" | "en";
export type SurfaceTheme = "light" | "dark";
export type SurfaceRole = "admin" | "operator" | "viewer";

export interface SurfaceViewport {
  width: number;
  height: number;
}

export interface SurfaceScenario {
  id: string;
  category: SurfaceCategory;
  scenario: string;
  language: SurfaceLanguage;
  viewport: SurfaceViewport;
  theme: SurfaceTheme;
  role: SurfaceRole;
}

export const DOCKER_STATES: readonly string[];
export const HISTORY_STATES: readonly string[];
export const DOCKER_ISOLATION: readonly string[];
export const HISTORY_ISOLATION: readonly string[];
export const ROLES: readonly SurfaceRole[];
export const SURFACE_CATEGORY_COUNTS: Readonly<Record<SurfaceCategory, number>>;
export function surfaceScenarioId(scenario: Omit<SurfaceScenario, "id">): string;
export const SURFACE_SCENARIOS: readonly SurfaceScenario[];
