import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TagChips } from "./tag-chips";

describe("TagChips", () => {
  it("adds a chip when Enter is pressed", async () => {
    const onChange = vi.fn();
    render(<TagChips value={[]} onChange={onChange} placeholder="添加标签" />);
    const input = screen.getByPlaceholderText("添加标签");
    await userEvent.type(input, "prod{Enter}");
    expect(onChange).toHaveBeenCalledWith(["prod"]);
  });

  it("removes a chip when the × is clicked", async () => {
    const onChange = vi.fn();
    render(<TagChips value={["prod", "web"]} onChange={onChange} />);
    const removeBtns = screen.getAllByRole("button", { name: /移除标签/i });
    await userEvent.click(removeBtns[0]);
    expect(onChange).toHaveBeenCalledWith(["web"]);
  });

  it("ignores duplicate tags", async () => {
    const onChange = vi.fn();
    render(<TagChips value={["prod"]} onChange={onChange} />);
    const input = screen.getByRole("textbox");
    await userEvent.type(input, "prod{Enter}");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("ignores empty input", async () => {
    const onChange = vi.fn();
    render(<TagChips value={[]} onChange={onChange} />);
    const input = screen.getByRole("textbox");
    await userEvent.type(input, "   {Enter}");
    expect(onChange).not.toHaveBeenCalled();
  });
});
