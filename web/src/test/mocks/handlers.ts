import { http, HttpResponse } from "msw";

const API_BASE = "/api/v1";

// Shared mock user for auth responses
const mockUser = {
  id: 1,
  username: "admin",
  role: "admin",
  totp_enabled: false,
};

// Shared mock token
const mockToken = "mock-jwt-token-for-testing";

export const handlers = [
  // POST /auth/login
  http.post(`${API_BASE}/auth/login`, async ({ request }) => {
    const body = (await request.json()) as { username?: string; password?: string };
    if (body.username === "admin" && body.password === "admin") {
      return HttpResponse.json({
        code: 200,
        message: "ok",
        data: {
          token: mockToken,
          user: mockUser,
        },
      });
    }
    return HttpResponse.json(
      { code: 401, message: "用户名或密码错误", data: null },
      { status: 401 },
    );
  }),

  // GET /auth/me
  http.get(`${API_BASE}/auth/me`, () => {
    return HttpResponse.json({
      code: 200,
      message: "ok",
      data: mockUser,
    });
  }),

  // GET /auth/captcha
  http.get(`${API_BASE}/auth/captcha`, () => {
    return HttpResponse.json({
      code: 200,
      message: "ok",
      data: { id: "test-captcha", question: "1+1=?", enabled: false },
    });
  }),

  // POST /tasks/statistics/query — empty historical series
  http.post(`${API_BASE}/tasks/statistics/query`, () => {
    return HttpResponse.json({
      code: 200,
      message: "ok",
      data: { series: [], step_seconds: 900, truncated: false },
    });
  }),

  // GET /overview — policy coverage only; retired node/resource counts are not part of the contract
  http.get(`${API_BASE}/overview`, () => {
    return HttpResponse.json({
      code: 200,
      message: "ok",
      data: {
        activePolicies: 0,
      },
    });
  }),
  // GET /nodes/:id/summary — open alerts and running tasks
  http.get(`${API_BASE}/nodes/:id/summary`, () => {
    return HttpResponse.json({
      code: 200,
      message: "ok",
      data: {
        open_alerts: 0,
        running_tasks: 0,
      },
    });
  }),
];
