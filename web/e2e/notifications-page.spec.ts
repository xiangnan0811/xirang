import { expect, test, type Locator, type Page } from "@playwright/test";

type UnreadCounts = { total: number; critical: number; warning: number };
type UnreadMode = "error" | "pending" | "zero" | "success";

const successCounts: UnreadCounts = { total: 7, critical: 2, warning: 5 };

type SharedUnread = {
  setMode: (next: UnreadMode) => void;
  release: () => void;
  next: () => Promise<UnreadCounts | "error">;
};

const scenarios = [
  {
    language: "zh" as const,
    width: 1280,
    height: 800,
    heading: "通知与告警",
    open: "待处理告警",
    critical: "严重告警",
    loading: "加载中...",
    failed: "告警统计加载失败",
    retry: "重试告警统计",
    pendingZero: "0 条待处理",
    successPending: "7 条待处理",
  },
  {
    language: "zh" as const,
    width: 390,
    height: 844,
    heading: "通知与告警",
    open: "待处理告警",
    critical: "严重告警",
    loading: "加载中...",
    failed: "告警统计加载失败",
    retry: "重试告警统计",
    pendingZero: "0 条待处理",
    successPending: "7 条待处理",
  },
  {
    language: "en" as const,
    width: 1280,
    height: 800,
    heading: "Notifications & Alerts",
    open: "Open Alerts",
    critical: "Critical Alerts",
    loading: "Loading...",
    failed: "Failed to load alert counts",
    retry: "Retry alert counts",
    pendingZero: "0 pending",
    successPending: "7 pending",
  },
  {
    language: "en" as const,
    width: 390,
    height: 844,
    heading: "Notifications & Alerts",
    open: "Open Alerts",
    critical: "Critical Alerts",
    loading: "Loading...",
    failed: "Failed to load alert counts",
    retry: "Retry alert counts",
    pendingZero: "0 pending",
    successPending: "7 pending",
  },
];

async function seedAdminSession(page: Page, language: "zh" | "en") {
  await page.addInitScript((selectedLanguage) => {
    sessionStorage.setItem("xirang-auth-token", "e2e-admin-token");
    sessionStorage.setItem("xirang-username", "admin");
    sessionStorage.setItem("xirang-role", "admin");
    sessionStorage.setItem("xirang-user-id", "1");
    sessionStorage.setItem("xirang-totp-enabled", "true");
    localStorage.setItem("xirang.language", selectedLanguage);
    localStorage.setItem(
      "xirang.setup-wizard",
      JSON.stringify({ completed: true, dismissed: true, currentStep: 0 }),
    );
  }, language);
}

function envelope(data: unknown, status = 200) {
  return {
    status,
    contentType: "application/json",
    body: JSON.stringify({
      code: status === 200 ? 0 : status,
      message: status === 200 ? "ok" : "unavailable",
      data,
    }),
  };
}

function paginated(data: unknown[]) {
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

function statCard(page: Page, title: string) {
  return page.locator("[data-tone]").filter({ has: page.getByText(title, { exact: true }) });
}

function pageHero(page: Page, heading: string) {
  return page.locator("header").filter({ has: page.getByRole("heading", { name: heading }) });
}

function createSharedUnread(): SharedUnread {
  let mode: UnreadMode = "error";
  const waiting: Array<() => void> = [];
  return {
    setMode(next: UnreadMode) {
      mode = next;
    },
    release() {
      const pending = waiting.splice(0);
      for (const resume of pending) {
        resume();
      }
    },
    async next(): Promise<UnreadCounts | "error"> {
      if (mode === "pending") {
        await new Promise<void>((resolve) => {
          waiting.push(resolve);
        });
      }
      if (mode === "error") {
        return "error";
      }
      if (mode === "success") {
        return successCounts;
      }
      return { total: 0, critical: 0, warning: 0 };
    },
  };
}

async function installNotificationsRoutes(page: Page, unread: SharedUnread) {
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;

    if (path.endsWith("/me/onboarded")) {
      await route.fulfill(envelope({ ok: true }));
      return;
    }
    if (path.endsWith("/auth/me") || path.endsWith("/auth/captcha")) {
      await route.fulfill(envelope({
        id: 1,
        username: "admin",
        role: "admin",
        totp_enabled: true,
        onboarded: true,
      }));
      return;
    }
    if (path.endsWith("/version/check")) {
      await route.fulfill(envelope({
        update_available: false,
        current_version: "e2e",
        latest_version: "e2e",
        release_url: "",
      }));
      return;
    }
    if (path.endsWith("/alerts/unread-count")) {
      const outcome = await unread.next();
      if (outcome === "error") {
        await route.fulfill(envelope({ total: 9, critical: 3, warning: 6 }, 500));
        return;
      }
      await route.fulfill(envelope(outcome));
      return;
    }
    if (path.includes("/delivery-stats")) {
      await route.fulfill(envelope({
        window_hours: 24,
        total_sent: 2,
        total_failed: 0,
        success_rate: 100,
        by_integration: [],
      }));
      return;
    }
    if (path.endsWith("/tasks/failure-summary")) {
      await route.fulfill(envelope({ failed_tasks: 0, window_hours: 24 }));
      return;
    }
    if (path.endsWith("/integrations")) {
      await route.fulfill(envelope([]));
      return;
    }
    if (path === "/api/v1/alerts") {
      await route.fulfill(paginated([]));
      return;
    }
    if (path === "/api/v1/tasks" || path === "/api/v1/nodes" || path === "/api/v1/policies" || path === "/api/v1/ssh-keys") {
      await route.fulfill(paginated([]));
      return;
    }
    if (path === "/api/v1/overview") {
      await route.fulfill(envelope({ activePolicies: 0 }));
      return;
    }
    await route.fulfill(envelope({}));
  });
}

async function expectNoHorizontalOverflow(page: Page, card: Locator, width: number) {
  const metrics = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.clientWidth + 1);
  expect(metrics.clientWidth).toBeLessThanOrEqual(width + 1);
  const box = await card.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(-1);
  expect(box!.x + box!.width).toBeLessThanOrEqual(width + 1);
}

for (const scenario of scenarios) {
  test(`unread counts show an error, then pending zero, without overflow in ${scenario.language} at ${scenario.width}x${scenario.height}`, async ({ page }) => {
    const unread = createSharedUnread();
    await page.setViewportSize({ width: scenario.width, height: scenario.height });
    await seedAdminSession(page, scenario.language);
    await installNotificationsRoutes(page, unread);

    await page.goto("/app/notifications");
    const open = statCard(page, scenario.open);
    const critical = statCard(page, scenario.critical);
    const hero = pageHero(page, scenario.heading);
    const retry = page.getByRole("button", { name: scenario.retry });

    await expect(open).toContainText(scenario.failed);
    await expect(open.locator(".tabular-nums")).toHaveText("—");
    await expect(critical.locator(".tabular-nums")).toHaveText("—");
    await expect(hero).toContainText(scenario.failed);
    await expect(hero).not.toContainText(scenario.pendingZero);
    await expect(retry).toBeVisible();
    await expectNoHorizontalOverflow(page, open, scenario.width);
    await expectNoHorizontalOverflow(page, retry, scenario.width);

    unread.setMode("pending");
    await retry.click();
    await expect(open).toContainText(scenario.loading);
    await expect(open.locator(".tabular-nums")).toHaveText("—");
    await expect(critical.locator(".tabular-nums")).toHaveText("—");
    await expect(open).not.toContainText(scenario.failed);
    await expect(hero).not.toContainText(scenario.pendingZero);
    await expectNoHorizontalOverflow(page, open, scenario.width);

    unread.setMode("zero");
    unread.release();
    await expect(open.locator(".tabular-nums")).toHaveText("0");
    await expect(critical.locator(".tabular-nums")).toHaveText("0");
    await expect(hero).toContainText(scenario.pendingZero);
    await expect(retry).toHaveCount(0);
    await expectNoHorizontalOverflow(page, open, scenario.width);
  });
}

for (const scenario of scenarios) {
  test(`unread counts retry an error into success 7/2/5 without overflow in ${scenario.language} at ${scenario.width}x${scenario.height}`, async ({ page }) => {
    const unread = createSharedUnread();
    await page.setViewportSize({ width: scenario.width, height: scenario.height });
    await seedAdminSession(page, scenario.language);
    await installNotificationsRoutes(page, unread);

    await page.goto("/app/notifications");
    const open = statCard(page, scenario.open);
    const critical = statCard(page, scenario.critical);
    const hero = pageHero(page, scenario.heading);
    const retry = page.getByRole("button", { name: scenario.retry });

    await expect(open).toContainText(scenario.failed);
    await expect(open.locator(".tabular-nums")).toHaveText("—");
    await expect(critical.locator(".tabular-nums")).toHaveText("—");
    await expect(hero).toContainText(scenario.failed);
    await expect(hero).not.toContainText(scenario.successPending);
    await expect(retry).toBeVisible();

    unread.setMode("success");
    await retry.click();

    await expect(open.locator(".tabular-nums")).toHaveText("7");
    await expect(critical.locator(".tabular-nums")).toHaveText("2");
    await expect(open).not.toContainText(scenario.failed);
    await expect(retry).toHaveCount(0);
    const destructiveHero = hero
      .locator("span", { hasText: scenario.successPending })
      .filter({ hasText: "destructive" });
    await expect(destructiveHero).toBeVisible();
    await expect(destructiveHero).toContainText(scenario.successPending);
    await expect(hero).not.toContainText(scenario.failed);
    await expect(hero).not.toContainText(scenario.pendingZero);
    await expectNoHorizontalOverflow(page, open, scenario.width);
    await expectNoHorizontalOverflow(page, critical, scenario.width);
    await expectNoHorizontalOverflow(page, destructiveHero, scenario.width);
  });
}
