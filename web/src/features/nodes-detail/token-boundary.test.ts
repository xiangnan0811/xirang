import { describe, expect, test } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

const featureDir = path.resolve(process.cwd(), "src/features/nodes-detail");

function collectProductionFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const fullPath = path.join(dir, entry);
    const stat = statSync(fullPath);
    if (stat.isDirectory()) {
      return collectProductionFiles(fullPath);
    }
    if (!/\.(ts|tsx)$/.test(entry) || /\.test\.(ts|tsx)$/.test(entry)) {
      return [];
    }
    return [fullPath];
  });
}

describe("node-detail auth token boundary", () => {
  test("production files do not read the auth token from browser storage", () => {
    const directStorageTokenReads = [
      {
        label: "sessionStorage auth token read",
        pattern: /\b(?:window\.)?sessionStorage\s*\.\s*getItem\s*\(\s*["']xirang-auth-token["']\s*\)/,
      },
      {
        label: "localStorage auth token read",
        pattern: /\b(?:window\.)?localStorage\s*\.\s*getItem\s*\(\s*["']xirang-auth-token["']\s*\)/,
      },
    ];

    const violations = collectProductionFiles(featureDir).flatMap((file) => {
      const source = readFileSync(file, "utf8");
      return directStorageTokenReads
        .filter(({ pattern }) => pattern.test(source))
        .map(({ label }) => `${path.relative(featureDir, file)}: ${label}`);
    });

    expect(violations).toEqual([]);
  });
});
