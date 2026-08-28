import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { SnapshotSearch } from "./snapshot-search";

const { searchFilesMock } = vi.hoisted(() => ({ searchFilesMock: vi.fn() }));

vi.mock("@/lib/api/client", () => ({
  apiClient: { searchFiles: searchFilesMock },
}));

describe("SnapshotSearch compatibility", () => {
  it("links only the task context and preserves the legacy file callback", () => {
    const onNavigateToFile = vi.fn();
    render(<SnapshotSearch taskId={73} token="auth-marker" onNavigateToFile={onNavigateToFile} />);

    const link = screen.getByRole("link", { name: /file workspace task context|文件工作区任务上下文/i });
    expect(link).toHaveAttribute("href", "/app/backups/data?taskId=73");
    expect(link.getAttribute("href")).not.toMatch(/snapshot|path|query|entryId|recoveryPointId/);
    expect(onNavigateToFile).not.toHaveBeenCalled();
  });
});
