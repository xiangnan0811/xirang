import "@testing-library/jest-dom/vitest";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { buildAssetRows } from "./__tests__/test-utils";
import { AssetList } from "./asset-list";
import { assetRefKey } from "./backup-assets-state";
import { runAxe } from "@/test/a11y-helpers";

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

    const list = screen.getByRole("list");
    expect(list.previousElementSibling).toHaveClass(
      "grid-cols-[44px_minmax(0,1fr)_72px_96px]"
    );
    const item = (await screen.findAllByRole("listitem"))[0];
    expect(item).toHaveClass("grid-cols-[44px_minmax(0,1fr)_72px_96px]");
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

    const item = (await screen.findAllByRole("listitem"))[0];
    const modified = item.querySelector("time");
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

    await waitFor(() => expect(screen.getAllByRole("listitem").length).toBeGreaterThan(0));
    expect(screen.getAllByRole("listitem").length).toBeLessThan(40);
  });

  it("keeps one roving activation stop and opens a file exactly once with Enter and Space", async () => {
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

    const activationButtons = rows.map((row) => screen.getByRole("button", { name: new RegExp(row.asset.name) }));
    expect(activationButtons.filter((button) => button.tabIndex === 0)).toHaveLength(1);
    activationButtons[0].focus();
    await user.keyboard("{ArrowDown}");
    expect(onActiveChange).toHaveBeenCalledWith(rows[1]);
    await user.keyboard("{Enter}");
    expect(onOpen).toHaveBeenCalledWith(rows[1], { index: 1, offset: 0 });
    expect(onOpen).toHaveBeenCalledTimes(1);
    await user.keyboard(" ");
    expect(onOpen).toHaveBeenCalledTimes(2);
    expect(onOpen).toHaveBeenLastCalledWith(rows[1], { index: 1, offset: 0 });
    expect(onSelectionToggle).not.toHaveBeenCalled();
    expect(activationButtons[1]).toHaveFocus();
  });

  it("keeps the bulk-selection checkbox beside the activation button and Space never opens the file", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(2);
    const onSelectionToggle = vi.fn();
    const onOpen = vi.fn();
    render(
      <AssetList
        rows={rows}
        selectedKeys={new Set()}
        activeKey={assetRefKey(rows[0].ref)}
        onActiveChange={vi.fn()}
        onSelectionToggle={onSelectionToggle}
        onOpen={onOpen}
      />
    );

    const item = (await screen.findAllByRole("listitem"))[1];
    const checkbox = screen.getAllByRole("checkbox")[1];
    const activation = screen.getByRole("button", { name: new RegExp(rows[1].asset.name) });
    expect(activation).not.toContainElement(checkbox);
    expect(item).toContainElement(checkbox);
    expect(item).toContainElement(activation);

    checkbox.focus();
    await user.keyboard(" ");
    expect(onSelectionToggle).toHaveBeenCalledOnce();
    expect(onSelectionToggle).toHaveBeenCalledWith(rows[1].ref);
    expect(onOpen).not.toHaveBeenCalled();

    await user.click(activation);
    expect(onOpen).toHaveBeenCalledOnce();
    expect(onSelectionToggle).toHaveBeenCalledOnce();
  });

  it("exposes current and selected state through axe-clean native controls", async () => {
    const rows = buildAssetRows(2);
    const { container } = render(
      <AssetList
        rows={rows}
        selectedKeys={new Set([assetRefKey(rows[1].ref)])}
        activeKey={assetRefKey(rows[0].ref)}
        onActiveChange={vi.fn()}
        onSelectionToggle={vi.fn()}
        onOpen={vi.fn()}
      />,
    );

    const current = await screen.findByRole("button", { name: new RegExp(rows[0].asset.name) });
    const selected = screen.getByRole("checkbox", { name: new RegExp(rows[1].asset.name) });
    expect(current).toHaveAttribute("aria-current", "true");
    expect(selected).toBeChecked();
    expect(await runAxe(container)).toHaveNoViolations();
  });

  it("shows a retained version count only on multi-version search rows", async () => {
    const [browse, singleBase] = buildAssetRows(2);
    const single = { ...singleBase, source: "search" as const, hitFields: ["name" as const], retainedVersionCount: 1 };
    const multi = {
      ...browse,
      ref: { ...browse.ref, entryId: "2".repeat(64) },
      source: "search" as const,
      hitFields: ["name" as const],
      retainedVersionCount: 2,
      asset: { ...browse.asset, name: "multi-version.yaml", ref: { ...browse.ref, entryId: "2".repeat(64) } },
    };
    render(
      <AssetList
        rows={[browse, single, multi]}
        selectedKeys={new Set()}
        activeKey={null}
        onActiveChange={vi.fn()}
        onSelectionToggle={vi.fn()}
        onOpen={vi.fn()}
      />
    );

    const items = await screen.findAllByRole("listitem");
    expect(items[0]).not.toHaveTextContent("个保留版本");
    expect(items[1]).not.toHaveTextContent("个保留版本");
    expect(items[2]).toHaveTextContent("2 个保留版本");
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
