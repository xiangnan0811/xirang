import type { Page, Request, Route } from "@playwright/test";
import { installWalkthroughMocks } from "../walkthrough/mock-handlers";
import type { ExpectedHttpError, UnknownRequest } from "../walkthrough/helpers";
import type { WalkthroughScenario } from "../walkthrough/scenarios.mjs";
import type { SurfaceRole, SurfaceScenario } from "./scenarios.mjs";

// Controlled HTTP for the Docker volume and task run history surfaces. These
// replies prove browser behavior for the declared data only; they are not
// backend, SSH, Docker CLI or daemon evidence.

export interface SurfaceIdentity {
  token: string;
  username: string;
  role: SurfaceRole;
  id: number;
}

export const IDENTITIES: Record<SurfaceRole, SurfaceIdentity> = {
  admin: { token: "surface-admin-token", username: "surface-admin", role: "admin", id: 11 },
  operator: { token: "surface-operator-token", username: "surface-operator", role: "operator", id: 12 },
  viewer: { token: "surface-viewer-token", username: "surface-viewer", role: "viewer", id: 13 },
};

export const LOGIN_PASSWORD = "surface-password";

export type Reply = { status: number; body: unknown };

export type ExchangeKind = "docker" | "runs";

// One intercepted request held until the test answers it. `transport` is what
// the browser network layer reported; it distinguishes a request the page
// aborted from one whose late response was delivered to the page. A
// `superseded` request is the first of the development StrictMode mount replay
// pair; the test answers the replayed request instead.
export interface Exchange {
  label: string;
  kind: ExchangeKind;
  pathname: string;
  search: URLSearchParams;
  identity: string;
  request: Request;
  superseded: boolean;
  replied: "none" | "fulfilled" | "rejected-by-browser";
  transport: "pending" | "finished" | "failed";
  failure: string | null;
  bodyConsumed: "yes" | "not-checked";
  respond: (reply: Reply) => Promise<void>;
}

export interface ExchangeEvidence {
  label: string;
  kind: ExchangeKind;
  pathname: string;
  search: string;
  identity: string;
  superseded: boolean;
  replied: Exchange["replied"];
  transport: Exchange["transport"];
  failure: string | null;
  bodyConsumed: Exchange["bodyConsumed"];
}

export interface SurfaceSession {
  exchanges: Exchange[];
  expectedHttpErrors: ExpectedHttpError[];
  unknownRequests: UnknownRequest[];
  authMeCalls: Array<{ identity: string }>;
  next: (kind: ExchangeKind) => Promise<Exchange>;
  current: (kind: ExchangeKind) => Promise<Exchange>;
  transportSettled: (exchange: Exchange) => Promise<void>;
  evidence: () => ExchangeEvidence[];
}

export function ok(data: unknown): Reply {
  return { status: 200, body: { code: 0, message: "ok", data } };
}

export function failure(status: number, message: string): Reply {
  return { status, body: { code: status, message, data: null } };
}

export function paginatedRuns(rows: unknown[], page: number, pageSize: number): Reply {
  const start = (page - 1) * pageSize;
  return {
    status: 200,
    body: { code: 0, message: "ok", data: rows.slice(start, start + pageSize), total: rows.length, page, page_size: pageSize },
  };
}

function identityForToken(header: string | null | undefined): SurfaceIdentity | null {
  const token = header?.replace(/^Bearer\s+/i, "") ?? "";
  return Object.values(IDENTITIES).find((identity) => identity.token === token) ?? null;
}

// The walkthrough mocks supply every shell, Nodes and Tasks request; the
// surface routes registered afterwards take precedence for the endpoints these
// states control. Session storage and /auth/me describe the same identity.
export async function installSurfaceMocks(page: Page, scenario: SurfaceScenario, runsById: Map<number, unknown>): Promise<SurfaceSession> {
  const shellScenario: WalkthroughScenario = {
    id: scenario.id,
    category: "route",
    path: "/app/nodes",
    language: scenario.language,
    viewport: scenario.viewport,
    theme: scenario.theme,
    role: "admin",
    scenario: "standard",
  };
  const shell = await installWalkthroughMocks(page, shellScenario);
  const exchanges: Exchange[] = [];
  const waiters: Array<{ kind: ExchangeKind; resolve: (exchange: Exchange) => void }> = [];
  const claimed: Record<ExchangeKind, number> = { docker: 0, runs: 0 };
  const settled = new Map<Request, () => void>();
  const authMeCalls: Array<{ identity: string }> = [];
  const expectedHttpErrors = shell.expectedHttpErrors;

  const settle = (request: Request, transport: Exchange["transport"], reason: string | null) => {
    const exchange = exchanges.find((entry) => entry.request === request);
    if (!exchange) return;
    exchange.transport = transport;
    exchange.failure = reason;
    settled.get(request)?.();
  };
  page.on("requestfinished", (request) => settle(request, "finished", null));
  page.on("requestfailed", (request) => settle(request, "failed", request.failure()?.errorText ?? "unknown failure"));

  const hold = (route: Route, kind: ExchangeKind) => {
    const request = route.request();
    const url = new URL(request.url());
    const identity = identityForToken(request.headers().authorization);
    const exchange: Exchange = {
      label: `${kind}#${exchanges.filter((entry) => entry.kind === kind).length + 1}`,
      kind,
      pathname: url.pathname,
      search: url.searchParams,
      identity: identity?.username ?? "unknown",
      request,
      superseded: false,
      replied: "none",
      transport: "pending",
      failure: null,
      bodyConsumed: "not-checked",
      respond: async (reply) => {
        if (reply.status >= 400) {
          expectedHttpErrors.push({ pathname: url.pathname, method: request.method(), status: reply.status });
        }
        try {
          await route.fulfill({
            status: reply.status,
            contentType: "application/json",
            headers: { "x-surface-exchange": exchange.label },
            body: JSON.stringify(reply.body),
          });
          exchange.replied = "fulfilled";
        } catch {
          // The browser already cancelled this request.
          exchange.replied = "rejected-by-browser";
        }
      },
    };
    exchanges.push(exchange);
    const index = waiters.findIndex((waiter) => waiter.kind === kind);
    if (index !== -1) {
      const [waiter] = waiters.splice(index, 1);
      claimed[kind] += 1;
      waiter.resolve(exchange);
    }
  };

  await page.route(/\/api\/v1\/nodes\/\d+\/docker-volumes(?:\?.*)?$/, (route) => hold(route, "docker"));
  await page.route(/\/api\/v1\/tasks\/\d+\/runs(?:\?.*)?$/, (route) => hold(route, "runs"));
  await page.route(/\/api\/v1\/nodes\/\d+\/files(?:\?.*)?$/, async (route) => {
    const path = new URL(route.request().url()).searchParams.get("path") ?? "/";
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(ok({ path, entries: [], truncated: false }).body) });
  });
  await page.route(/\/api\/v1\/task-runs\/\d+(?:\/logs)?(?:\?.*)?$/, async (route) => {
    const url = new URL(route.request().url());
    const match = /\/task-runs\/(\d+)(\/logs)?$/.exec(url.pathname);
    const run = match ? runsById.get(Number(match[1])) : undefined;
    if (!match || !run) {
      await route.fallback();
      return;
    }
    const reply = match[2]
      ? ok([{ id: 1, task_id: 1, task_run_id: Number(match[1]), level: "info", message: `surface run ${match[1]} log`, created_at: "2026-10-04T05:00:00Z" }])
      : ok(run);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(reply.body) });
  });
  await page.route("**/api/v1/auth/me", async (route) => {
    const identity = identityForToken(await route.request().headerValue("authorization"));
    authMeCalls.push({ identity: identity?.username ?? "unknown" });
    const reply = identity
      ? ok({ id: identity.id, username: identity.username, role: identity.role, totp_enabled: true, onboarded: true })
      : failure(401, "unauthorized");
    await route.fulfill({ status: reply.status, contentType: "application/json", body: JSON.stringify(reply.body) });
  });
  await page.route("**/api/v1/auth/login", async (route) => {
    let body: unknown = null;
    try {
      body = route.request().postDataJSON();
    } catch {
      body = null;
    }
    const username = body && typeof body === "object" && "username" in body ? body.username : null;
    const password = body && typeof body === "object" && "password" in body ? body.password : null;
    const identity = Object.values(IDENTITIES).find((entry) => entry.username === username);
    if (!identity || password !== LOGIN_PASSWORD) {
      await route.fulfill({ status: 599, contentType: "application/json", body: JSON.stringify(failure(599, "surface_unexpected_login").body) });
      return;
    }
    const reply = ok({ token: identity.token, user: { id: identity.id, username: identity.username, role: identity.role, totp_enabled: true } });
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(reply.body) });
  });

  const next = (kind: ExchangeKind) => new Promise<Exchange>((resolve) => {
    const available = exchanges.filter((entry) => entry.kind === kind);
    if (available.length > claimed[kind]) {
      resolve(available[claimed[kind]]);
      claimed[kind] += 1;
      return;
    }
    waiters.push({ kind, resolve });
  });

  return {
    exchanges,
    expectedHttpErrors,
    unknownRequests: shell.unknownRequests,
    authMeCalls,
    next,
    // The request that answers the current UI action. Development StrictMode
    // replays a mount's effects in the same commit, so a replayed request
    // arrives within the same frames and supersedes the first.
    current: async (kind) => {
      let exchange = await next(kind);
      await page.evaluate(() => new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(resolve, 150)));
      }));
      while (exchanges.filter((entry) => entry.kind === kind).length > claimed[kind]) {
        exchange.superseded = true;
        exchange = await next(kind);
      }
      return exchange;
    },
    transportSettled: (exchange) => new Promise<void>((resolve) => {
      if (exchange.transport !== "pending") {
        resolve();
        return;
      }
      settled.set(exchange.request, resolve);
    }),
    evidence: () => exchanges.map((exchange) => ({
      label: exchange.label,
      kind: exchange.kind,
      pathname: exchange.pathname,
      search: exchange.search.toString(),
      identity: exchange.identity,
      superseded: exchange.superseded,
      replied: exchange.replied,
      transport: exchange.transport,
      failure: exchange.failure,
      bodyConsumed: exchange.bodyConsumed,
    })),
  };
}

// Seeds one consistent identity before the first document; later navigations
// inside the same tab keep whatever the product stored.
export async function installSurfaceEnvironment(page: Page, scenario: SurfaceScenario): Promise<SurfaceIdentity> {
  const identity = IDENTITIES[scenario.role];
  await page.setViewportSize(scenario.viewport);
  await page.addInitScript(({ language, theme, identity: seeded }) => {
    // Mark only intercepted bodies the application actually reads. The header
    // is fixture-only and does not alter the JSON response contract.
    const surfaceWindow = window as Window & { __xirangSurfaceConsumed?: string[] };
    surfaceWindow.__xirangSurfaceConsumed = [];
    const originalText = Response.prototype.text;
    Response.prototype.text = async function (this: Response) {
      const body = await originalText.call(this);
      const label = this.headers.get("x-surface-exchange");
      if (label) surfaceWindow.__xirangSurfaceConsumed?.push(label);
      return body;
    };
    if (sessionStorage.getItem("xirang-surface-seeded") === "1") {
      return;
    }
    sessionStorage.setItem("xirang-surface-seeded", "1");
    localStorage.setItem("xirang.language", language);
    localStorage.setItem("xirang-theme", theme);
    localStorage.setItem(
      "xirang.setup-wizard",
      JSON.stringify({ completed: true, dismissed: true, currentStep: 0 }),
    );
    sessionStorage.setItem("xirang-auth-token", seeded.token);
    sessionStorage.setItem("xirang-username", seeded.username);
    sessionStorage.setItem("xirang-role", seeded.role);
    sessionStorage.setItem("xirang-user-id", String(seeded.id));
    sessionStorage.setItem("xirang-totp-enabled", "true");
  }, { language: scenario.language, theme: scenario.theme, identity });
  return identity;
}
