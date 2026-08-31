import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const forbidden = /\b(?:text|bg|border)-(?:emerald|red|amber|sky)-(?:400|500|600)\b/;
const allowed: Record<string, true> = {
  [path.join("src", "pages", "dashboards", "panel-renderer.tsx")]: true,
};

function walk(dir: string, files: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = path.join(dir, entry);
    const stat = statSync(full);
    if (stat.isDirectory()) {
      if (entry === "node_modules" || entry === "__tests__") continue;
      walk(full, files);
      continue;
    }
    if (!/\.(ts|tsx)$/.test(entry)) continue;
    if (entry.includes(".test.") || entry.includes(".spec.")) continue;
    files.push(full);
  }
  return files;
}

describe("status color palettes", () => {
  it("does not use raw Tailwind status classes outside documented exceptions", () => {
    const root = path.join(process.cwd(), "src");
    const hits: string[] = [];
    for (const file of walk(root)) {
      const relative = path.relative(process.cwd(), file);
      if (allowed[relative]) continue;
      if (relative.includes(`${path.sep}terminal`) || relative.includes("web-terminal")) continue;
      const source = readFileSync(file, "utf8");
      if (forbidden.test(source)) {
        hits.push(relative);
      }
    }
    expect(hits).toEqual([]);
  });
});
