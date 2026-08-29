import { expect, test, type Locator, type Page } from "@playwright/test";

import fixture from "../src/lib/api/__fixtures__/backup-assets.fixture.json" with { type: "json" };

const fileSourceNodeId = 17;
const fileSourceSetId = "12121212121212121212121212121212";
const liveRoute =
  `/app/backups/data?nodeId=${fileSourceNodeId}&backupSetId=${fileSourceSetId}` +
  `&repositoryId=${fixture.ids.onlineRepository}&taskId=71` +
  `&recoveryPointId=${fixture.ids.onlineRecoveryPoint}`;

async function seedAdminSession(page: Page, language: "zh" | "en" = "zh") {
  await page.addInitScript((selectedLanguage) => {
    sessionStorage.setItem("xirang-auth-token", "e2e-admin-token");
    sessionStorage.setItem("xirang-username", "admin");
    sessionStorage.setItem("xirang-role", "admin");
    sessionStorage.setItem("xirang-user-id", "1");
    sessionStorage.setItem("xirang-totp-enabled", "true");
    localStorage.setItem("xirang.language", selectedLanguage);
    localStorage.setItem(
      "xirang.setup-wizard",
      JSON.stringify({ completed: true, dismissed: true, currentStep: 0 })
    );
  }, language);
}

function envelope(data: unknown, status = 200) {
  return {
    status,
    contentType: "application/json",
    body: JSON.stringify({ code: status === 200 ? 0 : status, message: status === 200 ? "ok" : "unavailable", data }),
  };
}

async function expectPreviewFrameToFillViewport(viewport: Locator, label: string) {
  await expect.poll(
    () => viewport.evaluate((node) => {
      const frame = node.querySelector("iframe");
      if (!(frame instanceof HTMLIFrameElement)) return 1_000_000;

      const style = window.getComputedStyle(node);
      const verticalInset =
        (Number.parseFloat(style.paddingTop) || 0) +
        (Number.parseFloat(style.paddingBottom) || 0);
      const availableHeight = node.getBoundingClientRect().height - verticalInset;
      return Math.abs(availableHeight - frame.getBoundingClientRect().height);
    }),
    { message: `${label} preview frame should fill the viewport content box` },
  ).toBeLessThanOrEqual(1);
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
    if (
      url.includes("/backup-file-sources/") ||
      url.includes("/backup-repositories") ||
      url.includes("/asset-search")
    ) {
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
    if (url.includes(`/backup-file-sources/nodes/${fileSourceNodeId}/sets`)) {
      await route.fulfill(envelope({
        items: [{
          backup_set_id: fileSourceSetId,
          node_id: fileSourceNodeId,
          display_label: "合成夜间备份 · Synthetic nightly archive",
          lineage_kind: "task",
          version_count: 1,
          latest_retained_at: "2026-07-19T00:05:00Z",
          catalog_coverage: "complete",
          browse_state: "browsable",
        }],
        next_cursor: null,
      }));
      return;
    }
    if (url.includes(`/backup-file-sources/sets/${fileSourceSetId}/versions`)) {
      await route.fulfill(envelope({
        items: [{
          recovery_point_id: fixture.ids.onlineRecoveryPoint,
          repository_id: fixture.ids.onlineRepository,
          producing_task_id: 71,
          captured_at: "2026-07-19T00:00:00Z",
          committed_at: "2026-07-19T00:05:00Z",
          created_at: "2026-07-19T00:00:00Z",
          lifecycle_state: "committed",
          catalog_coverage: "complete",
          browse_state: "browsable",
          content_availability: { available: true, reason: null },
          entry_count: 240,
          logical_bytes: 8388608,
          permissions: { list: true, preview: false, download: false },
        }],
        next_cursor: null,
      }));
      return;
    }
    if (url.includes("/backup-file-sources/nodes")) {
      await route.fulfill(envelope({
        items: [{
          node_id: fileSourceNodeId,
          display_name: "合成节点 · Synthetic node",
          backup_set_count: 1,
          retained_version_count: 1,
          latest_retained_at: "2026-07-19T00:05:00Z",
          catalog_coverage: "complete",
          browse_state: "browsable",
        }],
        next_cursor: null,
      }));
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
          renderer: "plain_text",
          profile: "text_v2",
          content_type: "text/plain; charset=utf-8",
          content_length: 128,
          truncated: false,
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
      const parentEntryId = new URL(url).searchParams.get("parent");
      await route.fulfill(envelope(parentEntryId === fixture.ids.directoryEntry
        ? {
            items: [],
            next_cursor: null,
            directory: {
              current: {
                recovery_point_id: fixture.ids.onlineRecoveryPoint,
                entry_id: fixture.ids.directoryEntry,
                name: "synthetic-directory",
              },
              parent: null,
              breadcrumb: [{
                recovery_point_id: fixture.ids.onlineRecoveryPoint,
                entry_id: fixture.ids.directoryEntry,
                name: "synthetic-directory",
              }],
            },
          }
        : {
            items: fixture.entries,
            next_cursor: null,
            directory: { current: null, parent: null, breadcrumb: [] },
          }));
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

function paginatedEnvelope(data: unknown[]) {
  return {
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({
      code: 0,
      message: "ok",
      data,
      total: data.length,
      page: 1,
      page_size: 20,
    }),
  };
}

function tooltipTask(id: number, name: string, executorType = "rsync") {
  return {
    id,
    name,
    status: executorType === "rsync" ? "retrying" : "success",
    node_id: 17,
    node: { id: 17, name: "Synthetic node" },
    executor_type: executorType,
    enabled: true,
    rsync_publication: executorType === "rsync"
      ? {
          mode: "legacy_mutable",
          state: "legacy",
          reason_code: "legacy",
          capability_revision: 1,
          task_revision: "1",
          seed_full_copy_required: false,
        }
      : undefined,
  };
}

async function mockTasksTooltip(page: Page) {
  const tasks = [
    tooltipTask(901, "tooltip-first"),
    tooltipTask(902, "filler-one", "command"),
    tooltipTask(903, "filler-two", "command"),
    tooltipTask(904, "tooltip-last"),
  ];

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/me/onboarded")) {
      await route.fulfill(envelope({ ok: true }));
      return;
    }
    if (url.pathname.endsWith("/auth/me") || url.pathname.endsWith("/auth/captcha")) {
      await route.fulfill(envelope({ id: 1, username: "admin", role: "admin", totp_enabled: true, onboarded: true }));
      return;
    }
    if (url.pathname === "/api/v1/tasks") {
      await route.fulfill(paginatedEnvelope(tasks));
      return;
    }
    if (url.pathname === "/api/v1/nodes" || url.pathname === "/api/v1/policies") {
      await route.fulfill(envelope([]));
      return;
    }
    await route.fulfill(envelope({}));
  });
}

async function expectTooltipInsideScrollport(action: Locator, trigger: "focus" | "hover") {
  const tooltip = action.locator('[role="tooltip"]');
  if (trigger === "focus") {
    await action.focus();
    await expect(action).toBeFocused();
  } else {
    await action.evaluate((node) => (node as HTMLElement).blur());
    await action.hover();
  }
  await expect(tooltip).toBeVisible();

  const geometry = await tooltip.evaluate((node) => {
    const scrollport = node.closest(".overflow-x-auto");
    if (!(scrollport instanceof HTMLElement)) return null;
    const rect = node.getBoundingClientRect();
    const clip = scrollport.getBoundingClientRect();
    return {
      top: rect.top,
      right: rect.right,
      bottom: rect.bottom,
      left: rect.left,
      clipTop: clip.top,
      clipRight: clip.right,
      clipBottom: clip.bottom,
      clipLeft: clip.left,
      overflowX: window.getComputedStyle(scrollport).overflowX,
      scrollWidth: scrollport.scrollWidth,
      clientWidth: scrollport.clientWidth,
    };
  });

  expect(geometry).not.toBeNull();
  expect(geometry!.overflowX).toBe("auto");
  expect(geometry!.scrollWidth).toBeGreaterThan(geometry!.clientWidth);
  expect(geometry!.top).toBeGreaterThanOrEqual(geometry!.clipTop - 1);
  expect(geometry!.right).toBeLessThanOrEqual(geometry!.clipRight + 1);
  expect(geometry!.bottom).toBeLessThanOrEqual(geometry!.clipBottom + 1);
  expect(geometry!.left).toBeGreaterThanOrEqual(geometry!.clipLeft - 1);
}

test("closed FeatureLive does not open a searchable workspace", async ({ page }) => {
  await seedAdminSession(page);
  await mockClosedFeature(page);
  await page.goto("/app/backups/data");
  await expect(page.getByText(/文件来源当前不可用|The file source is currently unavailable/)).toBeVisible();
  await expect(page.getByRole("searchbox")).toHaveCount(0);
});

test("live FeatureLive can browse, search, and preview fixtures at a usable height", async ({ page }) => {
  await seedAdminSession(page);
  await mockLiveFeature(page);
  await page.goto(liveRoute);
  await expect(page.getByText(/文件浏览功能未启用|File browsing is not enabled/)).toHaveCount(0);
  await expect(page.getByRole("list", { name: /File list|文件列表/ })).toBeVisible();
  await expect(page.getByText("合成审计日志-synthetic-audit.log")).toBeVisible();

  const search = page.getByRole("searchbox", { name: /Search files|搜索文件/ });
  await expect(search).toBeVisible();
  const searchPosted = page.waitForRequest(
    (request) => request.method() === "POST" && request.url().includes("/asset-search")
  );
  await search.fill("synthetic-audit");
  await search.press("Enter");
  await searchPosted;
  await expect(page.getByText("合成审计日志-synthetic-audit.log")).toBeVisible();

  const previewPosted = page.waitForRequest(
    (request) => request.method() === "POST" && request.url().includes("/delivery-tickets")
  );
  await page.getByRole("button", { name: /(?:Open file or directory|打开文件或目录).*synthetic-audit/ }).click();
  const previewRequest = await previewPosted;
  expect(previewRequest.postDataJSON()).toEqual({
    schema_version: 1,
    action: "preview",
    preview_intent: "safe_preview_v1",
  });
  await expect(page.getByRole("button", { name: /Load preview|加载预览/ })).toHaveCount(0);
  const viewport = page.getByTestId("asset-preview-viewport");
  await expect(viewport).toBeVisible();
  await expect(viewport.locator("iframe")).toBeVisible();
  await expect(viewport.frameLocator("iframe").locator("body")).toContainText(
    "Synthetic escaped preview fixture"
  );

  await expectPreviewFrameToFillViewport(viewport, "split");

  await page.getByRole("button", { name: /Focused reading|专注阅读/ }).click();
  await expect(page.getByRole("button", { name: /Exit focused reading|退出专注阅读/ })).toBeVisible();
  await expectPreviewFrameToFillViewport(viewport, "focused");
});

test("directory Up navigation restores origin focus on mobile at 200% text zoom", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await seedAdminSession(page);
  await mockLiveFeature(page);
  await page.goto(liveRoute);
  await expect(page.getByRole("list", { name: /File list|文件列表/ })).toBeVisible();
  await page.evaluate(() => { document.documentElement.style.fontSize = "200%"; });

  const directory = page.getByRole("button", { name: /(?:Open file or directory|打开文件或目录).*synthetic-directory/ });
  await directory.focus();
  await directory.press("Enter");
  await expect(page).toHaveURL(new RegExp(`parentEntryId=${fixture.ids.directoryEntry}`));
  const up = page.getByRole("button", { name: /Up one directory|返回上级目录/ });
  await expect(up).toBeEnabled();
  await up.click();

  await expect(page).not.toHaveURL(/parentEntryId=/);
  await expect(directory).toBeFocused();
  await expect(page.getByText("合成审计日志-synthetic-audit.log")).toBeVisible();
});

for (const scenario of [
  {
    language: "zh" as const,
    firstName: "接入或刷新任务 tooltip-first 的文件预览",
    lastName: "接入或刷新任务 tooltip-last 的文件预览",
  },
  {
    language: "en" as const,
    firstName: "Connect or refresh file preview for task tooltip-first",
    lastName: "Connect or refresh file preview for task tooltip-last",
  },
]) {
  test(`task table keeps first and last disabled tooltips inside the horizontal scrollport in ${scenario.language}`, async ({ page }) => {
    await page.setViewportSize({ width: 900, height: 720 });
    await seedAdminSession(page, scenario.language);
    await page.addInitScript(() => {
      localStorage.setItem("xirang.tasks.view", JSON.stringify("list"));
    });
    await mockTasksTooltip(page);
    await page.goto("/app/tasks");

    const table = page.getByRole("table");
    await expect(table).toBeVisible();
    const firstAction = page.getByRole("button", { name: scenario.firstName });
    const lastAction = page.getByRole("button", { name: scenario.lastName });

    for (const action of [firstAction, lastAction]) {
      await expect(action).toHaveAttribute("aria-disabled", "true");
      await expect(action).toHaveAttribute("aria-describedby", /task-preview-connect-tooltip-table-/);
      await expectTooltipInsideScrollport(action, "focus");
      await expectTooltipInsideScrollport(action, "hover");
    }
  });
}
