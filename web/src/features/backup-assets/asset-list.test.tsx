import "@testing-library/jest-dom/vitest";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { buildAssetRows } from "./__tests__/test-utils";
import { AssetList } from "./asset-list";
import { assetRefKey } from "./backup-assets-state";

beforeAll(patchElementMeasurements);

describe("AssetList", () => {
  it("uses shrinkable tracks that fit the workspace minimum width", async () => {
    render(
      <AssetList
        rows={buildAssetRows(1)}
        selectedKeys={new Set()}
        activeKey={null}
        onActiveChange={vi.fn()}
        onSelectionToggle={vi.fn()}
        onOpen={vi.fn()}
      />
    );

    const listbox = screen.getByRole("listbox");
    expect(listbox.previousElementSibling).toHaveClass(
      "grid-cols-[36px_minmax(0,1fr)_72px_96px]"
    );
    const option = (await screen.findAllByRole("option"))[0];
    expect(option).toHaveClass("grid-cols-[36px_minmax(0,1fr)_72px_96px]");
  });

  it("shows a compact modified time while retaining the full value", async () => {
    render(
      <AssetList
        rows={buildAssetRows(1)}
        selectedKeys={new Set()}
        activeKey={null}
        onActiveChange={vi.fn()}
        onSelectionToggle={vi.fn()}
        onOpen={vi.fn()}
      />
    );

    const option = (await screen.findAllByRole("option"))[0];
    const modified = option.querySelector("time");
    expect(modified).toHaveTextContent("07-19 00:00");
    expect(modified).toHaveAttribute("dateTime", "2026-07-19T00:00:00Z");
    expect(modified).toHaveAttribute("title", "2026-07-19 00:00");
  });

  it("virtualizes a large result set to a bounded number of rows", async () => {
    render(
      <AssetList
        rows={buildAssetRows(1000)}
        selectedKeys={new Set()}
        activeKey={null}
        onActiveChange={vi.fn()}
        onSelectionToggle={vi.fn()}
        onOpen={vi.fn()}
      />
    );

    await waitFor(() => expect(screen.getAllByRole("option").length).toBeGreaterThan(0));
    expect(screen.getAllByRole("option").length).toBeLessThan(40);
  });

  it("uses one tab stop and supports keyboard selection and opening", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(6);
    const onActiveChange = vi.fn();
    const onSelectionToggle = vi.fn();
    const onOpen = vi.fn();
    render(
      <AssetList
        rows={rows}
        selectedKeys={new Set()}
        activeKey={assetRefKey(rows[0].ref)}
        onActiveChange={onActiveChange}
        onSelectionToggle={onSelectionToggle}
        onOpen={onOpen}
      />
    );

    const options = await screen.findAllByRole("option");
    expect(options.filter((option) => option.tabIndex === 0)).toHaveLength(1);
    options[0].focus();
    await user.keyboard("{ArrowDown}");
    expect(onActiveChange).toHaveBeenCalledWith(rows[1]);
    await user.keyboard(" ");
    expect(onSelectionToggle).toHaveBeenCalledWith(rows[1].ref);
    await user.keyboard("{Enter}");
    expect(onOpen).toHaveBeenCalledWith(rows[1], { index: 1, offset: 0 });
  });

  it("uses the option itself for pointer selection without nesting a checkbox", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(2);
    const onSelectionToggle = vi.fn();
    render(
      <AssetList
        rows={rows}
        selectedKeys={new Set()}
        activeKey={assetRefKey(rows[0].ref)}
        onActiveChange={vi.fn()}
        onSelectionToggle={onSelectionToggle}
        onOpen={vi.fn()}
      />
    );

    const option = (await screen.findAllByRole("option"))[0];
    expect(option.querySelector('[role="checkbox"]')).toBeNull();
    await user.click(option);
    expect(onSelectionToggle).toHaveBeenCalledWith(rows[0].ref);
  });
});

function patchElementMeasurements() {
  const rectangle = {
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: 800,
    bottom: 480,
    width: 800,
    height: 480,
    toJSON: () => ({}),
  } as DOMRect;
  Object.defineProperties(HTMLElement.prototype, {
    getBoundingClientRect: { configurable: true, value: () => rectangle },
    offsetHeight: { configurable: true, get: () => 480 },
    offsetWidth: { configurable: true, get: () => 800 },
    clientHeight: { configurable: true, get: () => 480 },
    clientWidth: { configurable: true, get: () => 800 },
  });
  fireEvent(window, new Event("resize"));
}
