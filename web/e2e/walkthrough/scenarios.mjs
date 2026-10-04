/**
 * Canonical walkthrough matrix. Spec and the Node runner/compiler import this
 * module; do not duplicate the 464 keys elsewhere.
 */

const ROUTE_PATHS = [
  "/login",
  "/app/overview",
  "/app/nodes",
  "/app/nodes/1?tab=overview",
  "/app/nodes/1?tab=tasks",
  "/app/nodes/1?tab=alerts",
  "/app/nodes/1?tab=profile",
  "/app/nodes/1?tab=anomaly",
  "/app/ssh-keys",
  "/app/policies",
  "/app/backups/overview",
  "/app/backups/data",
  "/app/backups/recovery",
  "/app/tasks",
  "/app/logs",
  "/app/notifications",
  "/app/automation-rules",
  "/app/audit",
  "/app/credential-audit",
  "/app/credential-access-grants",
  "/app/credentials",
  "/app/reports?tab=sla",
  "/app/reports?tab=slo",
  "/app/settings?tab=personal",
  "/app/settings?tab=account",
  "/app/settings?tab=users",
  "/app/settings?tab=channels",
  "/app/settings?tab=silences",
  "/app/settings?tab=escalation",
  "/app/settings?tab=system",
  "/app/settings?tab=maintenance",
  "/app/more",
];

const LANGUAGES = ["zh", "en"];
const THEMES = ["light", "dark"];
const VIEWPORTS = [
  { width: 1440, height: 900 },
  { width: 1024, height: 768 },
  { width: 390, height: 844 },
];
const DESKTOP = { width: 1440, height: 900 };

const DIALOGS = [
  ["NodeEditorDialog", "/app/nodes"],
  ["SSHKeyEditorDialog", "/app/ssh-keys"],
  ["SSHKeyRotationWizard", "/app/ssh-keys"],
  ["CredentialEditorDialog", "/app/credentials"],
  ["PolicyEditorDialog", "/app/policies"],
  ["TaskCreateDialog", "/app/tasks"],
  ["NasMountWizard", "/app/backups/overview"],
  ["IntegrationCreateDialog", "/app/settings?tab=channels"],
  ["EscalationPolicyEditor", "/app/settings?tab=escalation"],
  ["AutomationRulesFormDialog", "/app/automation-rules"],
  ["ReportConfigDialog", "/app/reports?tab=sla"],
  ["SLODialog", "/app/reports?tab=slo"],
];

const EDGES = [
  ["empty_state", "/app/nodes"],
  ["empty_state", "/app/ssh-keys"],
  ["empty_state", "/app/policies"],
  ["empty_state", "/app/tasks"],
  ["empty_state", "/app/notifications"],
  ["empty_state", "/app/automation-rules"],
  ["empty_state", "/app/credentials"],
  ["empty_state", "/app/audit"],
  ["api_error_500", "/app/nodes"],
  ["api_error_500", "/app/policies"],
  ["api_error_500", "/app/tasks"],
  ["api_error_500", "/app/notifications"],
  ["api_error_500", "/app/overview"],
  ["extreme_long_strings", "/app/nodes"],
  ["extreme_long_strings", "/app/tasks"],
];

function createScenario(fields) {
  const viewport = { width: fields.viewport.width, height: fields.viewport.height };
  return {
    id: JSON.stringify([
      fields.category,
      fields.path,
      fields.language,
      `${viewport.width}x${viewport.height}`,
      fields.theme,
      fields.role,
      fields.scenario,
    ]),
    category: fields.category,
    path: fields.path,
    language: fields.language,
    viewport,
    theme: fields.theme,
    role: fields.role,
    scenario: fields.scenario,
  };
}

function buildScenarios() {
  const scenarios = [];

  for (const path of ROUTE_PATHS) {
    for (const language of LANGUAGES) {
      for (const viewport of VIEWPORTS) {
        for (const theme of THEMES) {
          scenarios.push(createScenario({
            category: "route",
            path,
            language,
            viewport,
            theme,
            role: path === "/login" ? "anonymous" : "admin",
            scenario: "standard",
          }));
        }
      }
    }
  }

  for (const [dialogName, path] of DIALOGS) {
    for (const language of LANGUAGES) {
      for (const theme of THEMES) {
        scenarios.push(createScenario({
          category: "dialog",
          path,
          language,
          viewport: DESKTOP,
          theme,
          role: "admin",
          scenario: dialogName,
        }));
      }
    }
  }

  for (const [edge, path] of EDGES) {
    for (const theme of THEMES) {
      scenarios.push(createScenario({
        category: "edge_state",
        path,
        language: "zh",
        viewport: DESKTOP,
        theme,
        role: "admin",
        scenario: edge,
      }));
    }
  }

  for (const theme of THEMES) {
    scenarios.push(createScenario({
      category: "auth_flow",
      path: "/login",
      language: "zh",
      viewport: DESKTOP,
      theme,
      role: "anonymous",
      scenario: "login_error",
    }));
  }

  return scenarios;
}

export const SCENARIOS = buildScenarios();

const ids = new Set(SCENARIOS.map((scenario) => scenario.id));
if (SCENARIOS.length !== 464 || ids.size !== 464) {
  throw new Error(`walkthrough matrix must declare 464 unique scenarios, got ${SCENARIOS.length} (${ids.size} unique)`);
}
