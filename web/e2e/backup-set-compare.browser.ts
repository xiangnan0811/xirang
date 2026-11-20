import { readFileSync } from "node:fs";
import path from "node:path";
import { expect, test, type Response } from "@playwright/test";

interface Descriptor {
  token: string;
  admin_id: number;
  admin_username: string;
  node_id: number;
  base_point_id: string;
  old_point_id: string;
  empty_point_id: string;
}

interface DiffPage {
  base_recovery_point_id: string;
  compare_recovery_point_id: string;
  base_content_availability: { available: boolean };
  compare_content_availability: { available: boolean };
  items: Array<{ base: DiffSide | null; compare: DiffSide | null }>;
  next_cursor?: string;
}

interface DiffSide {
  name: string;
  breadcrumb: Array<{ name: string }>;
}

function isDescriptor(value: unknown): value is Descriptor {
  return typeof value === "object" && value !== null
    && "token" in value && typeof value.token === "string"
    && "admin_username" in value && typeof value.admin_username === "string"
    && "admin_id" in value && typeof value.admin_id === "number"
    && "node_id" in value && typeof value.node_id === "number"
    && "base_point_id" in value && typeof value.base_point_id === "string" && /^[0-9a-f]{32}$/.test(value.base_point_id)
    && "old_point_id" in value && typeof value.old_point_id === "string" && /^[0-9a-f]{32}$/.test(value.old_point_id)
    && "empty_point_id" in value && typeof value.empty_point_id === "string" && /^[0-9a-f]{32}$/.test(value.empty_point_id);
}

function isDiffSide(value: unknown): value is DiffPage["items"][number]["base"] {
  return value === null || (typeof value === "object" && "name" in value && typeof value.name === "string"
    && "breadcrumb" in value && Array.isArray(value.breadcrumb)
    && value.breadcrumb.every((part: unknown) => typeof part === "object" && part !== null && "name" in part && typeof part.name === "string"));
}

function parseDiffPage(value: unknown): DiffPage {
  if (typeof value !== "object" || value === null || !("data" in value) || typeof value.data !== "object" || value.data === null) throw new Error("invalid comparison envelope");
  const data = value.data;
  if (!("base_recovery_point_id" in data) || typeof data.base_recovery_point_id !== "string"
    || !("compare_recovery_point_id" in data) || typeof data.compare_recovery_point_id !== "string"
    || !("base_content_availability" in data) || typeof data.base_content_availability !== "object" || data.base_content_availability === null
    || !("available" in data.base_content_availability) || typeof data.base_content_availability.available !== "boolean"
    || !("compare_content_availability" in data) || typeof data.compare_content_availability !== "object" || data.compare_content_availability === null
    || !("available" in data.compare_content_availability) || typeof data.compare_content_availability.available !== "boolean"
    || !("items" in data) || !Array.isArray(data.items)
    || !data.items.every((item: unknown) => typeof item === "object" && item !== null && "base" in item && "compare" in item && isDiffSide(item.base) && isDiffSide(item.compare))
    || ("next_cursor" in data && data.next_cursor !== undefined && typeof data.next_cursor !== "string")) throw new Error("invalid comparison page");
  return {
    base_recovery_point_id: data.base_recovery_point_id,
    compare_recovery_point_id: data.compare_recovery_point_id,
    base_content_availability: { available: data.base_content_availability.available },
    compare_content_availability: { available: data.compare_content_availability.available },
    items: data.items,
    next_cursor: "next_cursor" in data && typeof data.next_cursor === "string" ? data.next_cursor : undefined,
  };
}

const directory = process.env.XIRANG_COMPARE_BROWSER_FIXTURE_DIR;
if (!directory) throw new Error("XIRANG_COMPARE_BROWSER_FIXTURE_DIR is required");
const parsedFixture: unknown = JSON.parse(readFileSync(path.join(directory, "descriptor.json"), "utf8"));
if (!isDescriptor(parsedFixture)) throw new Error("invalid browser fixture descriptor");
const fixture = parsedFixture;

function isDiff(response: Response): boolean {
  return response.request().method() === "POST" && /\/api\/v1\/backup-file-sources\/sets\/[0-9a-f]{32}\/diffs$/.test(new URL(response.url()).pathname);
}

for (const scenario of [
  { language: "en", width: 1440, height: 900, entry: "Compare recovery points", base: "Base recovery point", comparison: "Comparison recovery point", moreVersions: "Load more versions", run: "Run comparison", results: "Comparison results", changed: "Changed files", moreChanges: "Load more changes", offline: "Content offline", close: "Close", loaded: (count: number) => `${count} changes loaded` },
  { language: "zh", width: 390, height: 844, entry: "比较恢复点", base: "基准恢复点", comparison: "对照恢复点", moreVersions: "加载更多版本", run: "运行比较", results: "比较结果", changed: "变更的文件", moreChanges: "加载更多变更", offline: "内容离线", close: "关闭", loaded: (count: number) => `已加载 ${count} 项变更` },
] as const) {
  test(`${scenario.language}: root comparison pages real Catalog results and restores focus`, async ({ page }) => {
    await page.setViewportSize({ width: scenario.width, height: scenario.height });
    await page.addInitScript(({ token, username, userId, language }) => {
      sessionStorage.setItem("xirang-auth-token", token);
      sessionStorage.setItem("xirang-username", username);
      sessionStorage.setItem("xirang-role", "admin");
      sessionStorage.setItem("xirang-user-id", String(userId));
      localStorage.setItem("xirang.language", language);
    }, { token: fixture.token, username: fixture.admin_username, userId: fixture.admin_id, language: scenario.language });
    await page.goto(`/app/backups/data?nodeId=${fixture.node_id}`);
    const setup = page.getByRole("dialog").filter({
      has: page.getByRole("button", { name: scenario.language === "en" ? "Start Setup" : "开始配置", exact: true }),
    });
    await expect(setup).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(setup).toBeHidden();
    const entry = page.getByRole("button", { name: scenario.entry, exact: true });
    await expect(entry).toBeEnabled();
    expect(new URL(page.url()).searchParams.has("entryId")).toBe(false);
    await entry.focus();
    await page.keyboard.press("Enter");

    const dialog = page.getByRole("dialog", { name: scenario.entry });
    await expect(dialog).toBeVisible();
    const base = dialog.getByLabel(scenario.base, { exact: true });
    const comparison = dialog.getByLabel(scenario.comparison, { exact: true });
    await expect(base).toBeFocused();
    await expect(base.locator(`option[value="${fixture.base_point_id}"]`)).toBeEnabled();
    await base.selectOption(fixture.base_point_id);
    await expect(comparison.locator(`option[value="${fixture.old_point_id}"]`)).toHaveCount(0);
    const versionPage = page.waitForResponse((response) => response.request().method() === "GET" && /\/backup-file-sources\/sets\/[0-9a-f]{32}\/versions$/.test(new URL(response.url()).pathname) && new URL(response.url()).searchParams.has("cursor"));
    await dialog.getByRole("button", { name: scenario.moreVersions, exact: true }).click();
    expect((await versionPage).status()).toBe(200);
    const oldOption = comparison.locator(`option[value="${fixture.old_point_id}"]`);
    await expect(oldOption).toBeEnabled();
    await expect(oldOption).toContainText(scenario.offline);
    await comparison.focus();
    await page.keyboard.press("End");
    await page.keyboard.press("Enter");
    await expect(comparison).toHaveValue(fixture.old_point_id);
    await expect(dialog.getByText(scenario.offline, { exact: true })).toBeVisible();

    const firstResponse = page.waitForResponse(isDiff);
    await dialog.getByRole("button", { name: scenario.run, exact: true }).click();
    const first = await firstResponse;
    expect(first.status()).toBe(200);
    const firstPage = parseDiffPage(await first.json());
    expect(firstPage.base_recovery_point_id).toBe(fixture.base_point_id);
    expect(firstPage.compare_recovery_point_id).toBe(fixture.old_point_id);
    expect(firstPage.base_content_availability.available).toBe(true);
    expect(firstPage.compare_content_availability.available).toBe(false);
    expect(firstPage.next_cursor).toBeTruthy();
    const result = dialog.getByRole("region", { name: scenario.results });
    const list = result.getByRole("list", { name: scenario.changed });
    await expect(result).toContainText(scenario.offline);
    await expect(result.getByRole("status")).toContainText(scenario.loaded(firstPage.items.length));
    await expect(list).toContainText(/alpha\s*\/\s*report\.txt/);
    await expect(list).toContainText(/beta\s*\/\s*report\.txt/);
    const paths = firstPage.items.flatMap((item) => {
      const side = item.base ?? item.compare;
      return side ? [[...side.breadcrumb.map((part) => part.name), side.name].join("/")] : [];
    });
    expect(paths).toContain("alpha/report.txt");
    expect(paths).toContain("beta/report.txt");
    const close = dialog.getByRole("button", { name: scenario.close, exact: true });
    await expect(close).toBeInViewport({ ratio: 1 });
    await page.screenshot({ path: test.info().outputPath("catalog-comparison.png"), animations: "disabled" });

    let total = firstPage.items.length;
    let cursor = firstPage.next_cursor;
    while (cursor) {
      const nextResponse = page.waitForResponse(isDiff);
      await result.getByRole("button", { name: scenario.moreChanges, exact: true }).click();
      const response = await nextResponse;
      expect(response.status()).toBe(200);
      const requestBody: unknown = response.request().postDataJSON();
      expect(typeof requestBody === "object" && requestBody !== null && "cursor" in requestBody && requestBody.cursor).toBe(cursor);
      const next = parseDiffPage(await response.json());
      total += next.items.length;
      cursor = next.next_cursor;
      await expect(result.getByRole("status")).toContainText(scenario.loaded(total));
    }
    expect(total).toBe(207);
    await expect(list).toContainText("z-change-204.txt");
    await expect(result.getByRole("button", { name: scenario.moreChanges, exact: true })).toHaveCount(0);
    await expect(close).toBeInViewport({ ratio: 1 });
    if (scenario.width === 390) {
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
    }

    await comparison.selectOption(fixture.empty_point_id);
    const emptyPointResponse = page.waitForResponse(isDiff);
    await dialog.getByRole("button", { name: scenario.run, exact: true }).click();
    const emptyPointResult = await emptyPointResponse;
    expect(emptyPointResult.status()).toBe(200);
    const emptyPointPage = parseDiffPage(await emptyPointResult.json());
    expect(emptyPointPage.compare_recovery_point_id).toBe(fixture.empty_point_id);
    expect(emptyPointPage.items.length).toBeGreaterThan(0);
    expect(emptyPointPage.items.every((item) => item.base !== null && item.compare === null)).toBe(true);
    if (scenario.width === 390) {
      await close.click();
    } else {
      await close.focus();
      await page.keyboard.press("Enter");
    }
    await expect(dialog).toHaveCount(0);
    await expect(entry).toBeFocused();
  });
}
