import "@testing-library/jest-dom/vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { PolicyEditorDialog } from "@/components/policy-editor-dialog";
import { bumpAuthSessionGeneration } from "@/lib/api/core";
import type { NodeRecord, PolicyRecord } from "@/types/domain";

const authFixture = vi.hoisted(() => ({ token: "FAKE_POLICY_EDITOR_TOKEN_FOR_TEST_ONLY" }));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

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
      getProfiles: mocks.getProfiles,
      getCredentials: mocks.getCredentials,
      triggerDrill: mocks.triggerDrill,
    },
    escalationApi: {
      listEscalationPolicies: mocks.listEscalationPolicies,
    },
    mocks,
  };
});

const apiMocks = apiFixture.mocks;

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      const labels: Record<string, string> = {
        "common.cancel": "Cancel",
        "common.saving": "Saving",
        "policyEditor.advancedSettings": "Advanced Settings",
        "policyEditor.appAwareBackup": "App-aware Backup",
        "policyEditor.appProfileNone": "No profile",
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
        "policyEditor.titleCreate": "Create Policy",
        "policyEditor.titleEdit": "Edit Policy {{name}}",
        "policyEditor.descEdit": "Edit policy {{name}}",
        "policyEditor.submitEdit": "Update",
        "policyEditor.relatedNodes": "Associated Nodes",
        "policyEditor.relatedNodesSelected": "{{count}} selected",
        "policyEditor.selectNode": "Select node {{name}}",
        "policyEditor.targetGuidance.create": "CREATE_TARGET_GUIDANCE",
        "policyEditor.targetGuidance.edit": "EDIT_TARGET_GUIDANCE",
        "policyEditor.targetGuidance.template": "TEMPLATE_TARGET_GUIDANCE",
        "policyEditor.drill.title": "Recovery Drill",
        "policyEditor.drill.unavailableTitle": "Recovery drills are unavailable",
        "policyEditor.drill.unavailableDesc": "Production restore drills are disabled.",
        "policyEditor.drill.enable": "Enable automatic recovery drill",
        "policyEditor.drill.trigger": "Trigger drill manually",
      };
      const label = labels[key] ?? key;
      return label.replace(/\{\{(\w+)\}\}/g, (_, name: string) => String(options?.[name] ?? ""));
    },
  }),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: authFixture.token }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiFixture.apiClient,
}));

vi.mock("@/lib/api/escalation", () => ({
  createEscalationApi: () => apiFixture.escalationApi,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

const nodes = [
  { id: 1, name: "alpha", host: "10.0.0.1", basePath: "/srv/alpha-base", backupDir: "alpha-dir" },
  { id: 2, name: "beta", host: "10.0.0.2", basePath: "/srv/beta-base", backupDir: "beta-dir" },
] as unknown as NodeRecord[];

const existingPolicy: PolicyRecord = {
  id: 7,
  name: "Nightly",
  sourcePath: "/data/app",
  targetPath: "/srv/stored-policy-target",
  cron: "0 2 * * *",
  naturalLanguage: "",
  enabled: true,
  criticalThreshold: 3,
  nodeIds: [1],
  verifyEnabled: false,
  verifySampleRate: 0,
  drillEnabled: false,
  drillCron: "",
  drillRestorePath: "",
  drillPreVerify: "",
  drillVerify: "",
  drillPostVerify: "",
  drillAutoCleanup: true,
};

const guessedPathPattern = /\/backup\/|alpha-dir|beta-dir|alpha-base|beta-base/;

describe("PolicyEditorDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authFixture.token = "FAKE_POLICY_EDITOR_TOKEN_FOR_TEST_ONLY";
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

  it.each(["close and reopen", "token change", "identity ABA"] as const)(
    "keeps current selectable options after %s when old responses arrive last",
    async (transition) => {
      const user = userEvent.setup();
      const oldProfiles = deferred<unknown[]>();
      const oldCredentials = deferred<unknown[]>();
      const oldEscalations = deferred<unknown[]>();
      apiMocks.getProfiles.mockReturnValueOnce(oldProfiles.promise).mockResolvedValue([
        { id: "mysql", name: "Current profile", credentialType: "mysql" },
      ]);
      apiMocks.getCredentials.mockReturnValueOnce(oldCredentials.promise).mockResolvedValue([
        { id: 2, name: "Current credential", type: "mysql" },
      ]);
      apiMocks.listEscalationPolicies.mockReturnValueOnce(oldEscalations.promise).mockResolvedValue([
        { id: 2, name: "Current escalation", enabled: true },
        { id: 3, name: "Disabled escalation", enabled: false },
      ]);
      const props = { onOpenChange: vi.fn(), onSave: vi.fn().mockResolvedValue(undefined) };
      const view = render(<PolicyEditorDialog {...props} open />);
      if (transition === "close and reopen") {
        view.rerender(<PolicyEditorDialog {...props} open={false} />);
      } else {
        authFixture.token = "FAKE_REPLACEMENT_TOKEN_FOR_TEST_ONLY";
        bumpAuthSessionGeneration();
      }
      view.rerender(<PolicyEditorDialog {...props} open />);
      if (transition === "identity ABA") {
        authFixture.token = "FAKE_POLICY_EDITOR_TOKEN_FOR_TEST_ONLY";
        bumpAuthSessionGeneration();
        view.rerender(<PolicyEditorDialog {...props} open />);
      }
      await screen.findByRole("option", { name: "Current profile" });
      await user.selectOptions(screen.getByLabelText("Select Profile"), "mysql");
      await user.selectOptions(screen.getByLabelText("Select Credential"), "2");
      await user.selectOptions(document.querySelector<HTMLSelectElement>("#policy-edit-escalation")!, "2");
      await act(async () => {
        oldProfiles.resolve([{ id: "old", name: "Old profile", credentialType: "mysql" }]);
        oldCredentials.resolve([{ id: 1, name: "Old credential", type: "mysql" }]);
        oldEscalations.resolve([{ id: 1, name: "Old escalation", enabled: true }]);
      });
      expect(screen.getByLabelText("Select Profile")).toHaveValue("mysql");
      expect(screen.getByLabelText("Select Credential")).toHaveValue("2");
      expect(document.querySelector("#policy-edit-escalation")).toHaveValue("2");
      expect(screen.getByRole("option", { name: "Current profile" })).toBeInTheDocument();
      expect(screen.getByRole("option", { name: "Current credential (mysql)" })).toBeInTheDocument();
      expect(screen.getByRole("option", { name: "Current escalation" })).toBeInTheDocument();
      expect(screen.queryByRole("option", { name: /Old|Disabled escalation/ })).not.toBeInTheDocument();
    },
  );

  it("tells new policies that saving assigns targets without guessing per-node paths", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(<PolicyEditorDialog open onOpenChange={vi.fn()} onSave={onSave} nodes={nodes} />);

    expect(screen.queryByText("CREATE_TARGET_GUIDANCE")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("Policy Name"), "Fresh");
    await user.type(screen.getByLabelText("Source Path"), "/data/fresh");
    await user.click(screen.getByRole("checkbox", { name: "Select node alpha" }));

    expect(screen.getByText("CREATE_TARGET_GUIDANCE")).toBeInTheDocument();
    expect(screen.queryByText(/EDIT_TARGET_GUIDANCE|TEMPLATE_TARGET_GUIDANCE/)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(guessedPathPattern);

    await user.click(screen.getByRole("button", { name: "Create" }));

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      name: "Fresh",
      sourcePath: "/data/fresh",
      targetPath: "/backup",
      nodeIds: [1],
    }));
    expect(onSave.mock.calls[0][0]).not.toHaveProperty("id");
  });

  it("guides existing policies to task editing as plain text and keeps the stored target in the payload", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(
      <PolicyEditorDialog open onOpenChange={vi.fn()} onSave={onSave} editingPolicy={existingPolicy} nodes={nodes} />,
    );

    expect(screen.getByText("EDIT_TARGET_GUIDANCE")).toBeInTheDocument();
    expect(screen.queryByText(/CREATE_TARGET_GUIDANCE|TEMPLATE_TARGET_GUIDANCE/)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(guessedPathPattern);

    const alpha = screen.getByRole("checkbox", { name: "Select node alpha" });
    await user.click(alpha);
    expect(alpha).not.toBeChecked();
    expect(screen.getByText("EDIT_TARGET_GUIDANCE")).toBeInTheDocument();
    await user.click(alpha);
    await user.click(screen.getByRole("checkbox", { name: "Select node beta" }));
    expect(document.body).not.toHaveTextContent(guessedPathPattern);

    await user.click(screen.getByRole("button", { name: "Update" }));

    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      id: 7,
      name: "Nightly",
      sourcePath: "/data/app",
      targetPath: "/srv/stored-policy-target",
      nodeIds: [1, 2],
    }));
  });

  it("does not claim that saving a template creates tasks or targets", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(
      <PolicyEditorDialog
        open
        onOpenChange={vi.fn()}
        onSave={onSave}
        editingPolicy={{ ...existingPolicy, isTemplate: true, nodeIds: [] }}
      />,
    );

    expect(screen.getByText("TEMPLATE_TARGET_GUIDANCE")).toBeInTheDocument();
    expect(screen.queryByText(/CREATE_TARGET_GUIDANCE|EDIT_TARGET_GUIDANCE/)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(guessedPathPattern);

    await user.click(screen.getByRole("button", { name: "Update" }));

    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      id: 7,
      targetPath: "/srv/stored-policy-target",
      nodeIds: [],
    }));
  });
});
