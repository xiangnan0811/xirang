import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Input } from "../input";

describe("Input", () => {
  it("renders a text input", () => {
    render(<Input placeholder="Enter value" />);
    expect(screen.getByPlaceholderText("Enter value")).toBeDefined();
  });

  it("reflects value prop", () => {
    render(<Input readOnly value="hello" />);
    const el = screen.getByDisplayValue("hello") as HTMLInputElement;
    expect(el.value).toBe("hello");
  });

  it("is disabled when disabled prop is set", () => {
    render(<Input disabled />);
    expect((screen.getByRole("textbox") as HTMLInputElement).disabled).toBe(true);
  });

  it("forwards type prop", () => {
    render(<Input type="email" placeholder="email" />);
    const el = screen.getByPlaceholderText("email") as HTMLInputElement;
    expect(el.type).toBe("email");
  });
});
