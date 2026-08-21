import { expect, test, type Page } from "@playwright/test";

import fixture from "../src/lib/api/__fixtures__/backup-assets.fixture.json" with { type: "json" };

const liveRoute =
  `/app/backups/data?repositoryId=${fixture.ids.onlineRepository}` +
  `&recoveryPointId=${fixture.ids.onlineRecoveryPoint}`;

async function seedAdminSession(page: Page) {
  await page.addInitScript(() => {
    sessionStorage.setItem("xirang-auth-token", "e2e-admin-token");
    sessionStorage.setItem("xirang-username", "admin");
    sessionStorage.setItem("xirang-role", "admin");
    sessionStorage.setItem("xirang-user-id", "1");
    sessionStorage.setItem("xirang-totp-enabled", "true");
    localStorage.setItem("xirang.language", "zh");
    localStorage.setItem(
      "xirang.setup-wizard",
      JSON.stringify({ completed: true, dismissed: true, currentStep: 0 })
    );
  });
}

function envelope(data: unknown, status = 200) {
  return {
    status,
    contentType: "application/json",
    body: JSON.stringify({ code: status === 200 ? 0 : status, message: status === 200 ? "ok" : "unavailable", data }),
  };
}

async function mockClosedFeature(page: Page) {
  await page.route("**/api/v1/**", async (route) => {
    const url = route.request().url();
    if (url.includes("/me/onboarded")) {
      await route.fulfill(envelope({ ok: true }));
      return;
    }
    if (url.includes("/auth/me") || url.includes("/auth/captcha")) {
      await route.fulfill(envelope({ id: 1, username: "admin", role: "admin", totp_enabled: true, onboarded: true }));
      return;
    }
    if (url.includes("/backup-repositories") || url.includes("/asset-search")) {
      await route.fulfill(
        envelope({ reason: { code: "feature_disabled", params: {} } }, 503)
      );
      return;
    }
    await route.fulfill(envelope({}));
  });
}

async function mockLiveFeature(page: Page) {
  await page.route("**/api/v1/**", async (route) => {
    const url = route.request().url();
    const method = route.request().method();
    if (url.includes("/me/onboarded")) {
      await route.fulfill(envelope({ ok: true }));
      return;
    }
    if (url.includes("/auth/me") || url.includes("/auth/captcha")) {
      await route.fulfill(envelope({ id: 1, username: "admin", role: "admin", totp_enabled: true, onboarded: true }));
      return;
    }
    if (method === "POST" && url.includes("/asset-search")) {
      await route.fulfill(
        envelope({
          query_generation: "d".repeat(64),
          indexes: [
            {
              recovery_point_id: fixture.ids.onlineRecoveryPoint,
              catalog_generation_id: "10101010101010101010101010101010",
              search_generation_id: "40404040404040404040404040404040",
              projection_revision: 1,
              coverage: "complete",
              staleness: "fresh",
            },
          ],
          items: fixture.entries.slice(0, 2).map((asset) => ({
            ref: { recovery_point_id: asset.recovery_point_id, entry_id: asset.entry_id },
            asset,
            hit_fields: ["name"],
            score: 100,
            snippet: null,
          })),
          next_cursor: null,
          total: 2,
          total_relation: "exact",
          authoritative_empty: false,
          coverage: { status: "complete" },
          suggestions: [],
          capabilities: { metadata: true, content: false },
          permissions: { list: true, secret_reveal: false },
        })
      );
      return;
    }
    if (method === "POST" && url.includes("/delivery-tickets")) {
      await route.fulfill(
        envelope({
          schema_version: 1,
          content_url: `/api/v1/asset-content/${"5".repeat(32)}`,
          action: "preview",
          renderer: "escaped_text",
          profile: "text_v1",
          content_type: "text/plain; charset=utf-8",
          content_length: 128,
          etag: '"e2e-ticket-v1"',
          last_modified: "2026-07-19T00:00:00Z",
          range: "none",
          classification: "non_secret",
          expires_at: new Date(Date.now() + 10 * 60 * 1000).toISOString(),
          idle_expires_at: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
          capability_reason: null,
          fallback_actions: [],
        })
      );
      return;
    }
    if (url.includes("/asset-content/")) {
      await route.fulfill({
        status: 200,
        contentType: "text/plain; charset=utf-8",
        body: "Synthetic escaped preview fixture",
      });
      return;
    }
    if (url.includes("/backup-repositories/") && url.includes("/recovery-points")) {
      await route.fulfill(envelope({ items: fixture.recoveryPoints.online, next_cursor: null }));
      return;
    }
    if (url.includes("/backup-repositories")) {
      await route.fulfill(envelope({ items: fixture.repositories, next_cursor: null }));
      return;
    }
    if (url.includes("/recovery-points/") && url.includes("/catalog-status")) {
      await route.fulfill(envelope(fixture.recoveryPoints.online[0].catalog));
      return;
    }
    const entryMatch = url.match(/\/recovery-points\/[0-9a-f]{32}\/entries\/([0-9a-f]{64})/);
    if (entryMatch) {
      const entry = fixture.entries.find((candidate) => candidate.entry_id === entryMatch[1]);
      await route.fulfill(entry ? envelope(entry) : envelope(null, 404));
      return;
    }
    if (url.includes("/recovery-points/") && url.includes("/entries")) {
      await route.fulfill(envelope({ items: fixture.entries, next_cursor: null }));
      return;
    }
    if (url.includes("/recovery-points/") && url.includes("/evidence")) {
      await route.fulfill(envelope(fixture.evidence));
      return;
    }
    if (/\/recovery-points\/[0-9a-f]{32}$/.test(url)) {
      await route.fulfill(envelope(fixture.recoveryPoints.online[0]));
      return;
    }
    if (
      url.includes("/asset-saved-searches") ||
      url.includes("/asset-favorites") ||
      url.includes("/asset-tags") ||
      url.includes("/asset-recent")
    ) {
      await route.fulfill(envelope({ items: [], next_cursor: null }));
      return;
    }
    await route.fulfill(envelope({}));
  });
}

test("closed FeatureLive does not open a searchable workspace", async ({ page }) => {
  await seedAdminSession(page);
  await mockClosedFeature(page);
  await page.goto("/app/backups/data");
  await expect(page.getByText(/备份资产功能未启用|Backup assets are not enabled/)).toBeVisible();
  await expect(page.getByRole("searchbox")).toHaveCount(0);
});

test("live FeatureLive can browse, search, and preview fixtures", async ({ page }) => {
  await seedAdminSession(page);
  await mockLiveFeature(page);
  await page.goto(liveRoute);
  await expect(page.getByText(/备份资产功能未启用|Backup assets are not enabled/)).toHaveCount(0);
  await expect(page.getByRole("listbox", { name: /Backup asset list|备份资产列表/ })).toBeVisible();
  await expect(page.getByText("合成审计日志-synthetic-audit.log")).toBeVisible();

  const search = page.getByRole("searchbox", { name: /Search backup assets|搜索备份资产/ });
  await expect(search).toBeVisible();
  const searchPosted = page.waitForRequest(
    (request) => request.method() === "POST" && request.url().includes("/asset-search")
  );
  await search.fill("synthetic-audit");
  await search.press("Enter");
  await searchPosted;
  await expect(page.getByText("合成审计日志-synthetic-audit.log")).toBeVisible();

  await page.getByRole("option", { name: /synthetic-audit/ }).dblclick();
  const loadPreview = page.getByRole("button", { name: /Load preview|加载预览/ });
  await expect(loadPreview).toBeVisible();
  const previewPosted = page.waitForRequest(
    (request) => request.method() === "POST" && request.url().includes("/delivery-tickets")
  );
  await loadPreview.click();
  await previewPosted;
  const viewport = page.getByTestId("asset-preview-viewport");
  await expect(viewport).toBeVisible();
  await expect(viewport.locator("iframe")).toBeVisible();
  await expect(viewport.frameLocator("iframe").locator("body")).toContainText(
    "Synthetic escaped preview fixture"
  );
});
