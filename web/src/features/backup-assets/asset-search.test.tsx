import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import { useState } from "react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { AssetSearch } from "./asset-search";

describe("AssetSearch", () => {
  it("keeps a temporary query in memory and submits the selected scope", async () => {
    const user = userEvent.setup();
    const onDraftChange = vi.fn();
    const onSearch = vi.fn();
    const storageWrite = vi.spyOn(Storage.prototype, "setItem");
    const historyWrite = vi.spyOn(window.history, "replaceState");
    function Harness() {
      const [draft, setDraft] = useState("");
      return (
        <AssetSearch
          draft={draft}
          scope="current"
          disabled={false}
          onDraftChange={(value) => {
            setDraft(value);
            onDraftChange(value);
          }}
          onScopeChange={vi.fn()}
          onSearch={onSearch}
        />
      );
    }
    render(<Harness />);

    const input = screen.getByRole("searchbox", { name: /Search files|搜索文件/ });
    await user.type(input, "private-name.yaml");
    expect(onDraftChange).toHaveBeenLastCalledWith("private-name.yaml");
    await user.click(screen.getByRole("button", { name: /Search$|搜索$/ }));
    expect(onSearch).toHaveBeenCalledWith("private-name.yaml", "current");
    expect(storageWrite).not.toHaveBeenCalled();
    expect(historyWrite).not.toHaveBeenCalled();
    storageWrite.mockRestore();
    historyWrite.mockRestore();
  });

  it("offers only current and all-retained scopes", () => {
    render(
      <AssetSearch
        draft="term"
        scope="all_retained"
        disabled={false}
        onDraftChange={vi.fn()}
        onScopeChange={vi.fn()}
        onSearch={vi.fn()}
      />
    );

    expect(screen.getByRole("option", { name: /Current recovery point|当前恢复点/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /All retained|所有保留/ })).toBeInTheDocument();
    expect(screen.queryByText(/latest/i)).not.toBeInTheDocument();
  });
});
