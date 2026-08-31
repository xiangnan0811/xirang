import { expect, test } from "@playwright/test";

const ADMIN_USERNAME = "admin";
const ADMIN_PASSWORD = process.env.E2E_ADMIN_PASSWORD ?? "FAKE_E2E_AdminPass2026!_FOR_TEST_ONLY";

interface MeResponse {
  status: number;
  body: {
    data?: {
      user?: {
        username?: string;
        role?: string;
      };
    };
  } | null;
}

interface TasksResponse {
  code?: number;
  data?: unknown[];
  total?: number;
}

test("authenticates through the Vite proxy and renders the real task inventory", async ({ page, baseURL }) => {
  await page.addInitScript(() => {
    localStorage.setItem("xirang.language", "en");
  });
  const proxyOrigin = new URL(baseURL ?? "http://127.0.0.1:4178").origin;
  const captchaResponsePromise = page.waitForResponse((response) => {
    const requestURL = new URL(response.url());
    return response.request().method() === "GET"
      && requestURL.origin === proxyOrigin
      && requestURL.pathname === "/api/v1/auth/captcha";
  });
  await page.goto("/login");
  const captchaResponse = await captchaResponsePromise;
  expect(captchaResponse.status()).toBe(200);
  expect(new URL(captchaResponse.url()).origin).toBe(proxyOrigin);

  await page.getByLabel("Username").fill(ADMIN_USERNAME);
  await page.getByLabel("Password").fill(ADMIN_PASSWORD);

  const loginResponsePromise = page.waitForResponse((response) => {
    const requestURL = new URL(response.url());
    return response.request().method() === "POST"
      && requestURL.origin === proxyOrigin
      && requestURL.pathname === "/api/v1/auth/login";
  });
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  const loginResponse = await loginResponsePromise;
  expect(loginResponse.status()).toBe(200);
  expect(new URL(loginResponse.url()).origin).toBe(proxyOrigin);
  await expect(page).toHaveURL(/\/app\/overview$/);

  const me = await page.evaluate<MeResponse>(async () => {
    const token = sessionStorage.getItem("xirang-auth-token");
    if (!token) {
      return { status: 0, body: null };
    }
    const response = await fetch("/api/v1/me", {
      headers: { Authorization: `Bearer ${token}` },
    });
    return {
      status: response.status,
      body: (await response.json()) as MeResponse["body"],
    };
  });
  expect(me.status).toBe(200);
  expect(me.body).toMatchObject({
    data: { user: { username: ADMIN_USERNAME, role: "admin" } },
  });

  const tasksResponsePromise = page.waitForResponse((response) => {
    const requestURL = new URL(response.url());
    return response.request().method() === "GET"
      && requestURL.origin === proxyOrigin
      && requestURL.pathname === "/api/v1/tasks";
  });
  await page.goto("/app/tasks");
  const tasksResponse = await tasksResponsePromise;
  expect(tasksResponse.status()).toBe(200);
  expect(new URL(tasksResponse.url()).origin).toBe(proxyOrigin);
  const tasksPayload = (await tasksResponse.json()) as TasksResponse;
  expect(tasksPayload).toMatchObject({ code: 200, data: [], total: 0 });

  await expect(page.locator("#main-content h1")).toHaveText("Tasks");
  await expect(page.locator("#main-content")).toContainText("0 tasks");
});
