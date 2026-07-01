import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";

const indexHtmlPath = path.resolve(__dirname, "..", "index.html");

function readIndexHtml(): string {
  return fs.readFileSync(indexHtmlPath, "utf-8");
}

describe("web/index.html PWA meta", () => {
  it("includes both mobile-web-app-capable and apple-mobile-web-app-capable", () => {
    const html = readIndexHtml();

    expect(html).toContain('name="mobile-web-app-capable"');
    expect(html).toContain('name="apple-mobile-web-app-capable"');
  });
});