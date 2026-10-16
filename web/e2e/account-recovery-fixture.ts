import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { randomBytes, createHmac } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test as base } from "@playwright/test";
import { freshAuthenticatorCode } from "./real-backend-session";
import type { RecoveryStage } from "./account-recovery-reporter";

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repository = path.dirname(web);
const helper = path.join(web, "e2e/account-recovery-process.py");
const scratch = path.join(repository, ".tmp/agent");
const delay = (milliseconds: number) => {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, milliseconds);
  return promise;
};
export const syntheticPassword = () => `Synthetic!R19-${randomBytes(20).toString("hex")}`;
export type Enrollment = { requires_reenrollment: true; recovery_token: string; enrollment: { secret: string; qr_url: string; enrollment_id: string; expires_at: string; issuer: string } };
export type Login = { token: string; user: { id: number; username: string; role: string }; requires_2fa?: boolean; login_token: string };
export type CLIResult = { exit: number; operationId: string | null; committed: boolean; rejected: boolean; terminalRejected: boolean; prompts: number };
type Binaries = { directory: string; server: string; cli: string };

function freshDirectory(): string {
  for (const directory of [path.join(repository, ".tmp"), scratch]) {
    if (!existsSync(directory)) mkdirSync(directory, { mode: 0o700 });
    const info = lstatSync(directory);
    if (info.isSymbolicLink() || !info.isDirectory()) throw new Error("Unsafe recovery scratch directory");
  }
  const directory = mkdtempSync(path.join(scratch, "r19-browser-"));
  if (path.dirname(realpathSync(directory)) !== realpathSync(scratch)) throw new Error("Recovery scratch escaped boundary");
  writeFileSync(path.join(directory, "owner"), "exclusive recovery browser fixture\n", { mode: 0o600 });
  return directory;
}

async function port(): Promise<number> {
  const socket = createServer();
  const listening = Promise.withResolvers<void>();
  socket.once("error", listening.reject);
  socket.listen(0, "127.0.0.1", listening.resolve);
  await listening.promise;
  const address = socket.address();
  if (!address || typeof address === "string") throw new Error("No isolated port");
  const closed = Promise.withResolvers<void>();
  socket.close(error => error ? closed.reject(error) : closed.resolve());
  await closed.promise;
  return address.port;
}

async function command(binary: string, args: string[], cwd: string): Promise<void> {
  const { promise, resolve, reject } = Promise.withResolvers<void>();
  const child = spawn(binary, args, { cwd, stdio: "ignore" });
  const timer = setTimeout(() => { child.kill("SIGKILL"); }, 180_000);
  child.once("error", () => { clearTimeout(timer); reject(new Error("Recovery fixture build could not start")); });
  child.once("exit", code => {
    clearTimeout(timer);
    if (code === 0) resolve();
    else reject(new Error("Recovery fixture build failed; private output suppressed"));
  });
  await promise;
}

async function terminate(child: ChildProcess | undefined): Promise<void> {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  const { promise, resolve } = Promise.withResolvers<void>();
  const timer = setTimeout(() => child.kill("SIGKILL"), 10_000);
  child.once("exit", () => { clearTimeout(timer); resolve(); });
  child.kill("SIGTERM");
  await promise;
}

export class RecoveryRuntime {
  stage: RecoveryStage = "runtime-start";
  lastHTTPStatus: number | undefined;
  readonly directory = freshDirectory();
  readonly adminPassword = syntheticPassword();
  readonly confirmation = randomBytes(24).toString("hex");
  readonly jwtSecret = randomBytes(48).toString("hex");
  backendURL = "";
  frontendURL = "";
  private server?: ChildProcess;
  private vite?: ChildProcess;
  private env: Record<string, string> = {};
  private backendPort = 0;
  constructor(private binaries: Binaries) {}

  async start(): Promise<void> {
    if (process.platform !== "linux") throw new Error("The recovery fixture requires Linux controlling PTYs and /proc listener ownership checks");
    this.backendPort = await port();
    let frontendPort = await port();
    while (frontendPort === this.backendPort) frontendPort = await port();
    this.backendURL = `http://127.0.0.1:${this.backendPort}`;
    this.frontendURL = `http://127.0.0.1:${frontendPort}`;
    this.env = {
      PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: this.directory,
      APP_ENV: "development", ENVIRONMENT: "development", GIN_MODE: "release",
      SERVER_ADDR: `127.0.0.1:${this.backendPort}`, DB_TYPE: "sqlite",
      SQLITE_PATH: path.join(this.directory, "xirang.db"), DB_BACKUP_DIR: path.join(this.directory, "web-backups"),
      ADMIN_INITIAL_PASSWORD: this.adminPassword, JWT_SECRET: this.jwtSecret,
      DATA_ENCRYPTION_KEY: randomBytes(32).toString("hex"),
      XIRANG_BREAK_GLASS_CONFIRMATION: this.confirmation,
      CRON_DB_BACKUP_DIR: path.join(this.directory, "cron-backups"),
      SSH_STRICT_HOST_KEY_CHECKING: "true", SSH_AUTO_ACCEPT_NEW_HOSTS: "false",
      SSH_KNOWN_HOSTS_PATH: path.join(this.directory, "known_hosts"),
      CORS_ALLOWED_ORIGINS: this.frontendURL, TRUSTED_PROXIES: "127.0.0.1",
      LOGIN_RATE_LIMIT: "1000", LOG_LEVEL: "error",
    };
    writeFileSync(this.env.SSH_KNOWN_HOSTS_PATH, "", { mode: 0o600 });
    await this.restart();
    this.vite = spawn(process.execPath, [path.join(web, "node_modules/vite/bin/vite.js"), "--host", "127.0.0.1", "--port", String(frontendPort), "--strictPort"], {
      cwd: web, stdio: "ignore", env: { PATH: this.env.PATH, HOME: this.directory, VITE_API_BASE_URL: "/api/v1", VITE_PROXY_TARGET: this.backendURL, VITE_DEV_API_DIRECT_URL: "http://127.0.0.1:1/api/v1", VITE_ENABLE_DEMO_MODE: "false" },
    });
    this.vite.on("error", () => undefined);
    await this.ready(this.frontendURL + "/login", this.vite);
  }

  private async ready(url: string, child: ChildProcess): Promise<void> {
    const deadline = Date.now() + 60_000;
    const expectedAddress = `0100007F:${Number(new URL(url).port).toString(16).toUpperCase().padStart(4, "0")}`;
    while (Date.now() < deadline) {
      if (child.exitCode !== null || child.signalCode !== null) throw new Error("Owned fixture service exited; private output suppressed");
      // Random-port selection alone has a close/bind race. Prove the listening
      // socket belongs to this exact child before ever contacting its URL.
      let ownedListener = false;
      try {
        const fdDirectory = `/proc/${child.pid}/fd`;
        const sockets = new Set(readdirSync(fdDirectory).flatMap(fd => {
          try { return [readlinkSync(path.join(fdDirectory, fd))]; } catch { return []; }
        }));
        ownedListener = readFileSync("/proc/net/tcp", "utf8").split("\n").some(line => {
          const columns = line.trim().split(/\s+/);
          return columns[1] === expectedAddress && columns[3] === "0A" && sockets.has(`socket:[${columns[9]}]`);
        });
      } catch { /* Child has not established its listener yet. */ }
      if (ownedListener) {
        try { if ((await fetch(url, { signal: AbortSignal.timeout(1000) })).ok) return; } catch { /* Owned service still starting. */ }
      }
      await delay(100);
    }
    throw new Error("Owned fixture service readiness deadline");
  }

  async stop(): Promise<void> { await terminate(this.server); this.server = undefined; }
  async restart(): Promise<void> {
    if (this.server) throw new Error("Stop and reap the owned Core before restart");
    this.server = spawn(this.binaries.server, [], { cwd: this.directory, env: this.env, stdio: "ignore" });
    this.server.on("error", () => undefined);
    await this.ready(this.backendURL + "/readyz", this.server);
  }
  async close(): Promise<void> {
    try { await terminate(this.vite); } finally { await this.stop(); rmSync(this.directory, { recursive: true, force: true }); }
  }

  sql<T extends object>(sql: string, parameters: unknown[] = []): T[] {
    const result = spawnSync("python3", [helper], { input: JSON.stringify({ mode: "sql", runtime: this.directory, sql, parameters }), encoding: "utf8", stdio: ["pipe", "pipe", "ignore"], timeout: 10_000 });
    if (result.status !== 0) throw new Error("Isolated SQLite operation failed; private output suppressed");
    return JSON.parse(result.stdout) as T[];
  }

  cli(username: string, resetPassword: boolean, resetTOTP: boolean, password?: string, wrongConfirmation = false, redirect = false): CLIResult {
    if (this.server) throw new Error("Offline CLI requires stop and reap of owned Core");
    const args = ["-username", username, "-reason", "Synthetic isolated browser acceptance", "-offline-confirmed", ...(resetPassword ? ["-reset-password"] : []), ...(resetTOTP ? ["-reset-totp"] : [])];
    const cliEnv = Object.fromEntries(Object.entries(this.env).filter(([key]) => key !== "ADMIN_INITIAL_PASSWORD"));
    const result = spawnSync("python3", [helper], { input: JSON.stringify({ mode: "pty", runtime: this.directory, binary: this.binaries.cli, args, env: cliEnv, confirmation: wrongConfirmation ? "synthetic-wrong-confirmation" : this.confirmation, password, redirect }), encoding: "utf8", stdio: ["pipe", "pipe", "ignore"], timeout: 50_000 });
    if (result.status !== 0) throw new Error("Isolated controlling PTY failed; private output suppressed");
    return JSON.parse(result.stdout) as CLIResult;
  }

  cliObservation(args: string[], absentDatabase = false): { exit: number | null; help: boolean; committed: boolean; notFound: boolean; noDatabaseCreated: boolean } {
    if (this.server) throw new Error("CLI observation requires stopped owned Core");
    const missing = path.join(this.directory, "must-not-create.db");
    const cliEnv = Object.fromEntries(Object.entries(this.env).filter(([key]) => key !== "ADMIN_INITIAL_PASSWORD"));
    const result = spawnSync(this.binaries.cli, args, { cwd: this.directory, env: { ...cliEnv, ...(absentDatabase ? { SQLITE_PATH: missing } : {}) }, stdio: ["ignore", "pipe", "pipe"], encoding: "utf8", timeout: 15_000 });
    const output = result.stdout ?? "";
    return { exit: result.status, help: output.includes("Usage:"), committed: output.includes("status=committed"), notFound: output.includes("status=not-found"), noDatabaseCreated: !existsSync(missing) };
  }

  async request<T>(route: string, body?: unknown, token?: string, method = body === undefined ? "GET" : "POST", extraHeaders: Record<string, string> = {}): Promise<{ status: number; data: T; noStore: boolean; cookie: boolean }> {
    if (!this.server || this.server.exitCode !== null || this.server.signalCode !== null) throw new Error("The owned Core is not running");
    let response: Response;
    try {
      response = await fetch(this.backendURL + "/api/v1" + route, { method, headers: { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}), ...extraHeaders }, ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: AbortSignal.timeout(15_000) });
    } catch { throw new Error("Owned recovery HTTP request failed; private diagnostic suppressed"); }
    this.lastHTTPStatus = response.status;
    const envelope = await response.json() as { data: T };
    return { status: response.status, data: envelope.data, noStore: (response.headers.get("cache-control") ?? "").includes("no-store"), cookie: response.headers.has("set-cookie") };
  }

  async login(username = "admin", password = this.adminPassword): Promise<Login> {
    this.stage = "identity-password";
    const result = await this.request<Login>("/auth/login", { username, password });
    if (result.status !== 200) throw new Error("Synthetic login failed");
    return result.data;
  }
  async activate(username = "admin", password = this.adminPassword): Promise<{ token: string; secret: string; recoveryCodes: string[] }> {
    const login = await this.login(username, password);
    this.stage = "identity-setup";
    const setup = await this.request<{ secret: string; enrollment_id: string }>("/auth/2fa/setup", {}, login.token);
    if (setup.status !== 200) throw new Error("Synthetic factor setup failed");
    this.stage = "identity-activation";
    const activation = await this.request<{ token: string; recovery_codes: string[] }>("/auth/2fa/verify", { code: await freshAuthenticatorCode(setup.data.secret), enrollment_id: setup.data.enrollment_id }, login.token);
    if (activation.status !== 200) throw new Error("Synthetic factor activation failed");
    return { token: activation.data.token, secret: setup.data.secret, recoveryCodes: activation.data.recovery_codes };
  }
  async normalLogin(username: string, password: string, secret: string): Promise<Login> {
    const pending = await this.login(username, password);
    this.stage = "identity-second-factor";
    const result = await this.request<Login>("/auth/2fa/login", { login_token: pending.login_token, totp_code: await freshAuthenticatorCode(secret) });
    if (result.status !== 200) throw new Error("Synthetic second factor login failed");
    return result.data;
  }
  expiredToken(token: string): string {
    const [header, encoded] = token.split(".");
    const claims = JSON.parse(Buffer.from(encoded, "base64url").toString()) as Record<string, unknown>;
    claims.exp = Math.floor(Date.now() / 1000) - 60;
    claims.iat = Math.floor(Date.now() / 1000) - 660;
    const body = `${header}.${Buffer.from(JSON.stringify(claims)).toString("base64url")}`;
    return `${body}.${createHmac("sha256", this.jwtSecret).update(body).digest("base64url")}`;
  }
}

export const test = base.extend<{ recovery: RecoveryRuntime }, { recoveryBinaries: Binaries }>({
  recoveryBinaries: [async ({ browserName }, consumeFixture) => {
    if (browserName !== "chromium") throw new Error("The recovery fixture requires the dedicated Chromium project");
    const directory = freshDirectory();
    const binaries = { directory, server: path.join(directory, "server"), cli: path.join(directory, "recover-account") };
    try {
      await command("go", ["build", "-o", binaries.server, "./cmd/server"], path.join(repository, "backend"));
      await command("go", ["build", "-o", binaries.cli, "./cmd/recover-account"], path.join(repository, "backend"));
      await consumeFixture(binaries);
    } finally { rmSync(directory, { recursive: true, force: true }); }
  }, { scope: "worker", timeout: 360_000 }],
  recovery: async ({ recoveryBinaries }, consumeFixture) => {
    const runtime = new RecoveryRuntime(recoveryBinaries);
    try { await runtime.start(); runtime.stage = "runtime-ready"; await consumeFixture(runtime); } finally { await runtime.close(); }
  },
});
