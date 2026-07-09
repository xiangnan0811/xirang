import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(path.join(process.cwd(), "src/index.css"), "utf8");

describe("global CSS motion and decoration guardrails", () => {
  it("does not add ambient shell glow layers", () => {
    expect(css).not.toContain(".app-shell-bg::after");
  });

  it("does not animate login background ambient decoration", () => {
    expect(css).not.toContain("aurora-drift");
  });

  it("keeps card lift transitions complete at the opt-in utility", () => {
    const cardLiftBlock = css.match(/\.card-lift\s*\{(?<body>[\s\S]*?)\n\s*\}/)?.groups?.body ?? "";

    expect(cardLiftBlock).toContain("transform");
    expect(cardLiftBlock).toContain("box-shadow");
    expect(cardLiftBlock).toContain("border-color");
    expect(cardLiftBlock).toContain("background-color");
  });
});
