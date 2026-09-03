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
        onSearch={vi.fn()}
      />
    );

    expect(screen.getByRole("option", { name: /Current recovery point|当前恢复点/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /All retained|所有保留/ })).toBeInTheDocument();
    expect(screen.queryByText(/latest/i)).not.toBeInTheDocument();
  });

  it("clears a submitted docker query without writing it into the URL", async () => {
    const user = userEvent.setup();
    const onSearch = vi.fn();
    const onDraftChange = vi.fn();
    const historyWrite = vi.spyOn(window.history, "replaceState");
    render(
      <AssetSearch
        draft="docker"
        submittedQuery="docker"
        scope="current"
        disabled={false}
        onDraftChange={onDraftChange}
        onSearch={onSearch}
      />
    );

    await user.click(screen.getByRole("button", { name: /Clear query|清空条件/ }));
    expect(onDraftChange).toHaveBeenCalledWith("");
    expect(onSearch).toHaveBeenCalledWith("", "current");
    expect(historyWrite).not.toHaveBeenCalled();
    historyWrite.mockRestore();
  });

  it("reflows query, scope, and actions instead of pinning them to one clipped row", () => {
    render(
      <AssetSearch
        draft="docker"
        submittedQuery="docker"
        scope="current"
        disabled={false}
        onDraftChange={vi.fn()}
        onSearch={vi.fn()}
      />
    );

    const form = screen.getByRole("search");
    expect(form).toHaveClass("flex-wrap");
    expect(form).not.toHaveClass("h-11", "h-28");
    expect(screen.getByRole("searchbox").parentElement).toHaveClass("basis-full", "sm:basis-0");
    expect(screen.getByRole("combobox", { name: /Search scope|搜索范围/ }).parentElement).toHaveClass(
      "sm:w-72",
      "lg:w-80",
    );
    expect(screen.getByRole("option", { name: /Current recovery point|当前恢复点/ })).toHaveTextContent(
      /Current recovery point|当前恢复点/,
    );
    expect(screen.getByRole("option", { name: /All retained recovery points|所有保留恢复点/ })).toHaveTextContent(
      /All retained recovery points|所有保留恢复点/,
    );
  });

  it("keeps scope as a local draft until Search is submitted", async () => {
    const user = userEvent.setup();
    const onSearch = vi.fn();
    render(
      <AssetSearch
        draft="term"
        scope="current"
        disabled={false}
        onDraftChange={vi.fn()}
        onSearch={onSearch}
      />,
    );

    await user.selectOptions(screen.getByRole("combobox", { name: /Search scope|搜索范围/ }), "all_retained");
    await user.click(screen.getByRole("button", { name: /Search$|搜索$/ }));
    expect(onSearch).toHaveBeenCalledWith("term", "all_retained");
  });

  it("shows Clear on an active search route and keeps it usable while saved mode is locked", async () => {
    const user = userEvent.setup();
    const onSearch = vi.fn();
    const onDraftChange = vi.fn();
    render(
      <AssetSearch
        draft=""
        submittedQuery={null}
        scope="current"
        disabled={false}
        locked
        searchActive
        onDraftChange={onDraftChange}
        onSearch={onSearch}
      />,
    );

    expect(screen.getByRole("searchbox", { name: /Search files|搜索文件/ })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: /Search scope|搜索范围/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Search$|搜索$/ })).toBeDisabled();
    const clear = screen.getByRole("button", { name: /Clear query|清空条件/ });
    expect(clear).toBeEnabled();
    await user.click(clear);
    expect(onDraftChange).toHaveBeenCalledWith("");
    expect(onSearch).toHaveBeenCalledWith("", "current");
  });

});
