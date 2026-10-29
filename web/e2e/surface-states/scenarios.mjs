/**
 * Canonical surface-state inventory for Docker volumes and task run history.
 * Separate from the 464-scenario walkthrough matrix; the spec and the evidence
 * compiler both import this module.
 */

const DESKTOP = { width: 1440, height: 900 };
const MOBILE = { width: 390, height: 844 };

const STATE_COMBOS = ["zh", "en"].flatMap((language) => ["light", "dark"].flatMap((theme) => (
  [DESKTOP, MOBILE].map((viewport) => ({ language, theme, viewport }))
)));
const ISOLATION_COMBOS = [
  { language: "zh", theme: "light", viewport: DESKTOP },
  { language: "en", theme: "dark", viewport: DESKTOP },
];
const ROLE_COMBOS = [
  { language: "en", theme: "light", viewport: DESKTOP },
  { language: "zh", theme: "dark", viewport: MOBILE },
];

export const DOCKER_STATES = ["loading_rows", "empty", "partial_rows", "partial_empty", "error", "long_values"];
export const HISTORY_STATES = ["paginated", "empty", "error_retry"];
export const DOCKER_ISOLATION = ["late_close", "node_switch", "token_aba"];
export const HISTORY_ISOLATION = ["late_page_close", "task_switch", "language_switch", "token_aba", "late_retry"];
export const ROLES = ["admin", "operator", "viewer"];

export const SURFACE_CATEGORY_COUNTS = {
  docker_state: 48,
  docker_role: 6,
  docker_isolation: 6,
  history_state: 24,
  history_isolation: 10,
};

export function surfaceScenarioId(scenario) {
  return JSON.stringify([
    scenario.category,
    scenario.scenario,
    scenario.language,
    `${scenario.viewport.width}x${scenario.viewport.height}`,
    scenario.theme,
    scenario.role,
  ]);
}

function scenario(category, name, combo, role = "admin") {
  const entry = {
    category,
    scenario: name,
    language: combo.language,
    viewport: { ...combo.viewport },
    theme: combo.theme,
    role,
  };
  return { id: surfaceScenarioId(entry), ...entry };
}

export const SURFACE_SCENARIOS = Object.freeze([
  ...DOCKER_STATES.flatMap((name) => STATE_COMBOS.map((combo) => scenario("docker_state", name, combo))),
  ...ROLES.flatMap((role) => ROLE_COMBOS.map((combo) => scenario("docker_role", "entry", combo, role))),
  ...DOCKER_ISOLATION.flatMap((name) => ISOLATION_COMBOS.map((combo) => scenario("docker_isolation", name, combo))),
  ...HISTORY_STATES.flatMap((name) => STATE_COMBOS.map((combo) => scenario("history_state", name, combo))),
  ...HISTORY_ISOLATION.flatMap((name) => ISOLATION_COMBOS.map((combo) => scenario("history_isolation", name, combo))),
].map((entry) => Object.freeze(entry)));
