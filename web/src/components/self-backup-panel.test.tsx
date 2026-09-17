import { act, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { BackupEntry } from "@/lib/api/system-api";
import { SelfBackupPanel } from "./self-backup-panel";

const { auth, listBackups } = vi.hoisted(() => ({
  auth: { token: "old-session", role: "admin" },
  listBackups: vi.fn(),
}));
vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => auth }));
vi.mock("@/lib/api/client", () => ({ apiClient: { listBackups } }));

it("aborts an old session list and does not show its late response in the new session", async () => {
  let resolveOld!: (value: BackupEntry[]) => void;
  const oldRequest = new Promise<BackupEntry[]>((resolve) => { resolveOld = resolve; });
  listBackups.mockReturnValueOnce(oldRequest).mockResolvedValueOnce([
    { filename: "current.db", size: 1, createdAt: "2026-09-17T00:00:00Z", sha256: "test" },
  ]);
  const { rerender } = render(<SelfBackupPanel />);
  const oldSignal: AbortSignal = listBackups.mock.calls[0][1].signal;
  auth.token = "new-session";
  rerender(<SelfBackupPanel />);
  expect(oldSignal.aborted).toBe(true);
  expect(await screen.findByText("current.db")).toBeInTheDocument();
  await act(async () => {
    resolveOld([{ filename: "stale.db", size: 1, createdAt: "2026-09-17T00:00:00Z", sha256: "test" }]);
  });
  expect(screen.queryByText("stale.db")).not.toBeInTheDocument();
});
