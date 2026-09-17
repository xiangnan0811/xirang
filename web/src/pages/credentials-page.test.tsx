import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { CredentialsPage } from "./credentials-page";
import type { AppCredential } from "@/lib/api/credentials";

const {
  authState,
  confirmMock,
  deleteMock,
  listMock,
} = vi.hoisted(() => ({
  authState: { token: "test-token" as string | null, role: "admin" },
  confirmMock: vi.fn(),
  deleteMock: vi.fn(),
  listMock: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) => {
      if (typeof options?.count === "number") {
        return `${key}:${options.count}`;
      }
      if (typeof options?.name === "string") {
        return `${key}:${options.name}`;
      }
      return key;
    },
  }),
  initReactI18next: { type: "3rdParty", init: vi.fn() },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authState,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/credential-editor-dialog", () => ({
  CredentialEditorDialog: ({
    editingCredential,
    open,
  }: {
    editingCredential?: AppCredential | null;
    open: boolean;
  }) =>
    open ? (
      <div role="dialog">
        {editingCredential ? editingCredential.name : "create-credential"}
      </div>
    ) : null,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("@/lib/api/credentials", () => ({
  createCredentialsApi: () => ({
    delete: deleteMock,
    list: listMock,
    listProfiles: vi.fn().mockResolvedValue([]),
  }),
}));

const credentials: AppCredential[] = [
  {
    id: 1,
    name: "Prod MySQL",
    type: "mysql",
    description: "Primary database",
    config: {},
    hasPassword: true,
    referenceCount: 2,
    createdAt: "2026-05-13T10:00:00Z",
    updatedAt: "2026-05-13T10:00:00Z",
  },
  {
    id: 2,
    name: "Docker Socket",
    type: "docker",
    description: "",
    config: {},
    hasPassword: false,
    referenceCount: 0,
    createdAt: "2026-05-13T11:00:00Z",
    updatedAt: "2026-05-13T11:00:00Z",
  },
];

describe("CredentialsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authState.token = "test-token";
    confirmMock.mockResolvedValue(true);
    deleteMock.mockResolvedValue(undefined);
  });

  it("ignores an old token response and clears inventory on logout", async () => {
    let resolveOld!: (rows: AppCredential[]) => void;
    listMock.mockReturnValueOnce(new Promise<AppCredential[]>((resolve) => { resolveOld = resolve; }));
    const { rerender } = render(<CredentialsPage />);
    listMock.mockResolvedValueOnce([{ ...credentials[1], name: "New account credential" }]);
    authState.token = "new-token";
    rerender(<CredentialsPage />);
    expect(await screen.findByText("New account credential")).toBeInTheDocument();
    await act(async () => { resolveOld(credentials); });
    expect(screen.queryByText("Prod MySQL")).not.toBeInTheDocument();
    authState.token = null;
    rerender(<CredentialsPage />);
    expect(screen.queryByText("New account credential")).not.toBeInTheDocument();
    expect(screen.queryByRole("status", { name: "common.loading" })).not.toBeInTheDocument();
    expect(listMock).toHaveBeenCalledTimes(2);
  });

  it("renders the loading workbench state before credentials resolve", () => {
    listMock.mockReturnValue(new Promise(() => undefined));

    render(<CredentialsPage />);

    expect(screen.getByRole("heading", { name: "credentials.pageTitle" })).toBeInTheDocument();
    expect(screen.getAllByText("credentials.pageDesc").length).toBe(2);
    expect(screen.getByText("common.loading")).toBeInTheDocument();
    expect(screen.getByText("credentials.surfaceTitle")).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "common.loading" })).toBeInTheDocument();
  });

  it("renders an empty workbench inventory with a create action", async () => {
    const user = userEvent.setup();
    listMock.mockResolvedValue([]);

    render(<CredentialsPage />);

    expect(await screen.findByText("credentials.empty")).toBeInTheDocument();
    expect(screen.getByText("credentials.emptyDesc")).toBeInTheDocument();
    expect(screen.getByText("common.all 0")).toBeInTheDocument();
    expect(screen.getByText("common.password 0")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "credentials.createBtn" })[0]);

    expect(screen.getByRole("dialog")).toHaveTextContent("create-credential");
  });

  it("renders credential metadata and table actions", async () => {
    const user = userEvent.setup();
    listMock.mockResolvedValue(credentials);

    render(<CredentialsPage />);

    expect(await screen.findByText("Prod MySQL")).toBeInTheDocument();
    expect(screen.getByText("Docker Socket")).toBeInTheDocument();
    expect(screen.getByText("common.all 2")).toBeInTheDocument();
    expect(screen.getByText("common.password 1")).toBeInTheDocument();
    expect(screen.getByText("credentials.references 1")).toBeInTheDocument();
    expect(screen.getByText("common.neverUsed 1")).toBeInTheDocument();
    expect(screen.getByText("MySQL")).toBeInTheDocument();
    expect(screen.getByText("Docker")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "common.edit" })[0]);

    expect(screen.getByRole("dialog")).toHaveTextContent("Prod MySQL");
  });

  it("confirms and deletes a credential from the inventory", async () => {
    const user = userEvent.setup();
    listMock.mockResolvedValue(credentials);

    render(<CredentialsPage />);

    expect(await screen.findByText("Prod MySQL")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "common.delete" })[0]);

    await waitFor(() => {
      expect(confirmMock).toHaveBeenCalledWith({
        title: "credentials.confirmDeleteTitle",
        description: "credentials.confirmDeleteDesc:Prod MySQL",
      });
      expect(deleteMock).toHaveBeenCalledWith("test-token", 1);
    });
  });
});
