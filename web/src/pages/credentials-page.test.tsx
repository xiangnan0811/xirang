import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { CredentialsPage } from "./credentials-page";
import type { AppCredentialResponse } from "@/lib/api/credentials";

const {
  confirmMock,
  deleteMock,
  listMock,
} = vi.hoisted(() => ({
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
  useAuth: () => ({ token: "test-token", role: "admin" }),
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
    editingCredential?: AppCredentialResponse | null;
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

const credentials: AppCredentialResponse[] = [
  {
    id: 1,
    name: "Prod MySQL",
    type: "mysql",
    description: "Primary database",
    config: {},
    has_password: true,
    reference_count: 2,
    created_at: "2026-05-13T10:00:00Z",
    updated_at: "2026-05-13T10:00:00Z",
  },
  {
    id: 2,
    name: "Docker Socket",
    type: "docker",
    description: "",
    config: {},
    has_password: false,
    reference_count: 0,
    created_at: "2026-05-13T11:00:00Z",
    updated_at: "2026-05-13T11:00:00Z",
  },
];

describe("CredentialsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    confirmMock.mockResolvedValue(true);
    deleteMock.mockResolvedValue(undefined);
  });

  it("renders the loading workbench state before credentials resolve", () => {
    listMock.mockReturnValue(new Promise(() => undefined));

    render(<CredentialsPage />);

    expect(screen.getByRole("heading", { name: "credentials.pageTitle" })).toBeInTheDocument();
    expect(screen.getByText("credentials.pageDesc")).toBeInTheDocument();
    expect(screen.getByText("credentials.loadingMeta")).toBeInTheDocument();
    expect(screen.getByText("credentials.surfaceTitle")).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "common.loading" })).toBeInTheDocument();
  });

  it("renders an empty workbench inventory with a create action", async () => {
    const user = userEvent.setup();
    listMock.mockResolvedValue([]);

    render(<CredentialsPage />);

    expect(await screen.findByText("credentials.empty")).toBeInTheDocument();
    expect(screen.getByText("credentials.emptyDesc")).toBeInTheDocument();
    expect(screen.getByText("credentials.totalMeta:0")).toBeInTheDocument();
    expect(screen.getByText("credentials.passwordMeta:0")).toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "credentials.createBtn" })[0]);

    expect(screen.getByRole("dialog")).toHaveTextContent("create-credential");
  });

  it("renders credential metadata and table actions", async () => {
    const user = userEvent.setup();
    listMock.mockResolvedValue(credentials);

    render(<CredentialsPage />);

    expect(await screen.findByText("Prod MySQL")).toBeInTheDocument();
    expect(screen.getByText("Docker Socket")).toBeInTheDocument();
    expect(screen.getByText("credentials.totalMeta:2")).toBeInTheDocument();
    expect(screen.getByText("credentials.passwordMeta:1")).toBeInTheDocument();
    expect(screen.getByText("credentials.referencedMeta:1")).toBeInTheDocument();
    expect(screen.getByText("credentials.unusedMeta:1")).toBeInTheDocument();
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
