import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { AssetBulkBar } from "./asset-bulk-bar";

describe("AssetBulkBar", () => {
  it("exposes only real local selection commands", async () => {
    const user = userEvent.setup();
    const onClear = vi.fn();
    const onInspect = vi.fn();
    const { rerender } = render(
      <AssetBulkBar count={2} onClear={onClear} onInspect={onInspect} />
    );

    expect(screen.getByRole("status")).toHaveTextContent(/2.*selected|已选择.*2/);
    expect(screen.getByRole("button", { name: /Inspect selected|检查所选/ })).toBeDisabled();
    expect(screen.queryByRole("button", { name: /Export|导出|Recover|恢复/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Clear selection|清除选择/ }));
    expect(onClear).toHaveBeenCalledTimes(1);

    rerender(<AssetBulkBar count={1} onClear={onClear} onInspect={onInspect} />);
    await user.click(screen.getByRole("button", { name: /Inspect selected|检查所选/ }));
    expect(onInspect).toHaveBeenCalledTimes(1);
  });
});
