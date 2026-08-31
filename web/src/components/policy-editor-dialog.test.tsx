import "@testing-library/jest-dom/vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { PolicyEditorDialog } from "@/components/policy-editor-dialog";

const apiFixture = vi.hoisted(() => {
  const deprecatedMethod = ["get", "Hook", "Templates"].join("");
  const mocks = {
    deprecatedHookTemplates: vi.fn(),
    listEscalationPolicies: vi.fn(),
    getProfiles: vi.fn(),
    getCredentials: vi.fn(),
    triggerDrill: vi.fn(),
  };
  return {
    apiClient: {
      [deprecatedMethod]: mocks.deprecatedHookTemplates,
      listEscalationPolicies: mocks.listEscalationPolicies,
      getProfiles: mocks.getProfiles,
      getCredentials: mocks.getCredentials,
      triggerDrill: mocks.triggerDrill,
    },
    mocks,
  };
});

const apiMocks = apiFixture.mocks;

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string) => {
      const labels: Record<string, string> = {
        "common.cancel": "Cancel",
        "common.saving": "Saving",
        "policyEditor.advancedSettings": "Advanced Settings",
        "policyEditor.appAwareBackup": "App-aware Backup",
        "policyEditor.appProfileNone": "No profile",
        "policyEditor.backupStorageInfo": "Backup storage info",
        "policyEditor.backupVerify": "Backup Verification",
        "policyEditor.cronExpression": "Cron Expression",
        "policyEditor.descCreate": "Create policy",
        "policyEditor.failureThreshold": "Failure Threshold",
        "policyEditor.hookTimeout": "Hook Timeout",
        "policyEditor.maxRetries": "Max Retries",
        "policyEditor.policyName": "Policy Name",
        "policyEditor.policyNamePlaceholder": "Policy Name",
        "policyEditor.policyStatus": "Policy Status",
        "policyEditor.postHook": "Post Hook",
        "policyEditor.postHookPlaceholder": "Post hook",
        "policyEditor.preHook": "Pre Hook",
        "policyEditor.preHookPlaceholder": "Pre hook",
        "policyEditor.retryBaseSeconds": "Retry Base Seconds",
        "policyEditor.retryPreview": "Retry preview:",
        "policyEditor.sampleRate": "Sample Rate",
        "policyEditor.selectCredential": "Select Credential",
        "policyEditor.selectProfile": "Select Profile",
        "policyEditor.sourcePath": "Source Path",
        "policyEditor.sourcePathPlaceholder": "/data",
        "policyEditor.submitCreate": "Create",
        "policyEditor.targetPath": "Target Path",
        "policyEditor.targetPathPlaceholder": "/backup",
        "policyEditor.titleCreate": "Create Policy",
        "policyEditor.drill.title": "Recovery Drill",
        "policyEditor.drill.unavailableTitle": "Recovery drills are unavailable",
        "policyEditor.drill.unavailableDesc": "Production restore drills are disabled.",
        "policyEditor.drill.enable": "Enable automatic recovery drill",
        "policyEditor.drill.trigger": "Trigger drill manually",
      };
      return labels[key] ?? key;
    },
  }),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "FAKE_POLICY_EDITOR_TOKEN_FOR_TEST_ONLY" }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiFixture.apiClient,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

describe("PolicyEditorDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMocks.deprecatedHookTemplates.mockResolvedValue([
      {
        id: "mysql",
        name: "MySQL",
        preHook: "mysqldump",
        postHook: "rm dump",
        description: "legacy template",
      },
    ]);
    apiMocks.listEscalationPolicies.mockResolvedValue([]);
    apiMocks.getProfiles.mockResolvedValue([]);
    apiMocks.getCredentials.mockResolvedValue([]);
  });

  it("opens without requesting or rendering deprecated hook templates", async () => {
    const user = userEvent.setup();

    render(
      <PolicyEditorDialog
        open
        onOpenChange={vi.fn()}
        onSave={vi.fn().mockResolvedValue(undefined)}
      />
    );

    await waitFor(() => {
      expect(apiMocks.getProfiles).toHaveBeenCalledWith("FAKE_POLICY_EDITOR_TOKEN_FOR_TEST_ONLY");
      expect(apiMocks.getCredentials).toHaveBeenCalledWith("FAKE_POLICY_EDITOR_TOKEN_FOR_TEST_ONLY");
      expect(apiMocks.listEscalationPolicies).toHaveBeenCalledWith("FAKE_POLICY_EDITOR_TOKEN_FOR_TEST_ONLY");
    });
    expect(apiMocks.deprecatedHookTemplates).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Advanced Settings" }));

    expect(screen.queryByText("Insert Template")).not.toBeInTheDocument();
  });

  it("does not present manual or scheduled drill execution as working", async () => {
    const user = userEvent.setup();
    render(
      <PolicyEditorDialog
        open
        onOpenChange={vi.fn()}
        onSave={vi.fn().mockResolvedValue(undefined)}
      />
    );

    await user.click(screen.getByRole("button", { name: "Recovery Drill" }));

    expect(screen.getByText("Recovery drills are unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Enable automatic recovery drill" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Trigger drill manually" })).not.toBeInTheDocument();
    expect(apiMocks.triggerDrill).not.toHaveBeenCalled();
  });

});
