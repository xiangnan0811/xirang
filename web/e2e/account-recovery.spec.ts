import { expect, type Page, type TestInfo } from "@playwright/test";
import { test, syntheticPassword, type Enrollment, type Login, type RecoveryRuntime } from "./account-recovery-fixture";
import { authenticatorCode, freshAuthenticatorCode } from "./real-backend-session";
import { redactRecoveryError } from "./account-recovery-reporter";

type UserState = { id: number; username: string; role: string; token_version: number; totp_enabled: number; totp_reenrollment_required: number; totp_secret: string; recovery_codes: string; password_hash: string; totp_enrollment_id: string; totp_enrollment_expires_at: string | null };
type Identity = { username: string; password: string; id: number; role: string; token: string; secret: string; codes: string[]; pending: string; proof: string; grantId: number; nodeId: number };
const verifyPath = "/api/v1/auth/recovery/enrollment/verify";

// Error context can contain fill() arguments even with trace disabled. Replace
// error text before Playwright's artifact teardown; the reporter also never
// forwards raw call logs. No assertions below print credential values.
test.beforeEach(async ({ page, recovery }) => {
  page.on("request", request => {
    const route = new URL(request.url()).pathname;
    if (route === "/api/v1/auth/login") recovery.stage = "browser-login";
    if (route === verifyPath) recovery.stage = "browser-verify";
    if (route === "/api/v1/auth/2fa/login") recovery.stage = "browser-normal-login";
  });
  page.on("response", response => {
    const route = new URL(response.url()).pathname;
    if (route === "/api/v1/auth/login" || route === verifyPath || route === "/api/v1/auth/2fa/login") {
      recovery.lastHTTPStatus = response.status();
      if (route === verifyPath && response.status() === 200) recovery.stage = "browser-codes";
    }
  });
});

test.afterEach(async ({ page, recovery }, info) => {
  info.annotations.push({ type: "recovery-stage", description: recovery.stage });
  if (recovery.lastHTTPStatus !== undefined) info.annotations.push({ type: "recovery-http", description: String(recovery.lastHTTPStatus) });
  for (const error of info.errors) redactRecoveryError(error);
  if (info.errors.length > 0 && !page.isClosed()) await page.goto("about:blank").catch(() => undefined);
});

function state(runtime: RecoveryRuntime, username: string): UserState {
  const rows = runtime.sql<UserState>("SELECT id,username,role,token_version,totp_enabled,totp_reenrollment_required,totp_secret,recovery_codes,password_hash,totp_enrollment_id,totp_enrollment_expires_at FROM users WHERE username=?", [username]);
  if (rows.length !== 1) throw new Error("Synthetic identity missing");
  return rows[0];
}

async function seed(runtime: RecoveryRuntime, role: "admin" | "operator" = "admin"): Promise<Identity> {
  const admin = await runtime.activate();
  const username = role === "admin" ? "admin" : "recovery-operator";
  const operatorPassword = syntheticPassword();
  const password = role === "admin" ? runtime.adminPassword : operatorPassword;
  runtime.stage = "seed-users";
  const operator = await runtime.request<{ id: number }>("/users", { username: "recovery-operator", password: operatorPassword, role: "operator" }, admin.token);
  expect(operator.status).toBe(201);
  const user = state(runtime, username);
  runtime.stage = "seed-node";
  // Node admission deliberately rejects loopback hosts. Use the reserved
  // .invalid namespace: this resource is persisted for ownership/grant checks,
  // never connected to, and cannot name a real remote node.
  const node = await runtime.request<{ id: number }>("/nodes", { name: "synthetic-recovery-node", host: "r19-recovery.invalid", port: 1, username: "synthetic", auth_type: "password", password: syntheticPassword(), backup_dir: "recovery-fixture", base_path: "/tmp", tags: "r19-synthetic-only" }, admin.token);
  expect(node.status).toBe(201);
  runtime.stage = "seed-owners";
  expect((await runtime.request(`/nodes/${node.data.id}/owners`, { user_id: user.id }, admin.token)).status).toBe(200);
  expect((await runtime.request(`/nodes/${node.data.id}/owners`, { user_id: operator.data.id }, admin.token)).status).toBe(200);
  const activation = role === "admin" ? admin : await runtime.activate(username, password);
  runtime.stage = "seed-proof";
  const proof = await runtime.request<{ proof: string }>("/auth/step-up", { code: await freshAuthenticatorCode(activation.secret), step_up_action: role === "admin" ? "config.export" : "batch_command.create" }, activation.token);
  expect(proof.status).toBe(200);
  runtime.stage = "seed-grant";
  const grant = role === "admin"
    ? await runtime.request<{ id: number }>("/credential-access-grants/config-export", { reason: "Synthetic recovery acceptance", requested_ttl_seconds: 1800 }, activation.token, "POST", { "X-Xirang-Step-Up": proof.data.proof })
    : await runtime.request<{ id: number }[]>("/credential-access-grants/batch-command", { node_ids: [node.data.id], reason: "Synthetic recovery acceptance", requested_ttl_seconds: 1800 }, activation.token, "POST", { "X-Xirang-Step-Up": proof.data.proof });
  expect(grant.status).toBe(201);
  const grantId = Array.isArray(grant.data) ? grant.data[0].id : grant.data.id;
  runtime.stage = "seed-pending";
  const pending = await runtime.login(username, password);
  expect(pending.requires_2fa).toBe(true);
  runtime.stage = "seed-assertions";
  expect(runtime.sql<{ n: number }>("SELECT COUNT(*) n FROM users WHERE role='admin'")[0].n).toBe(1);
  return { username, password, id: user.id, role, ...activation, codes: activation.recoveryCodes, pending: pending.login_token, proof: proof.data.proof, grantId, nodeId: node.data.id };
}

async function offlineReset(runtime: RecoveryRuntime, identity: Identity, password = false, totp = true, newPassword?: string): Promise<void> {
  runtime.stage = "offline-stop";
  await runtime.stop();
  runtime.stage = "offline-reset";
  const outcome = runtime.cli(identity.username, password, totp, newPassword);
  expect(outcome.exit).toBe(0);
  expect(outcome.committed).toBe(true);
  expect(outcome.prompts).toBe(password ? 3 : 1);
  expect(Boolean(outcome.operationId)).toBe(true);
  runtime.stage = "offline-status";
  expect(runtime.cliObservation(["-status", outcome.operationId!]).committed).toBe(true);
  runtime.stage = "offline-restart";
  await runtime.restart();
}

async function browserPassword(page: Page, runtime: RecoveryRuntime, identity: Pick<Identity, "username" | "password">, language = "en", redirect = "/app/nodes"): Promise<Enrollment | Login> {
  await page.addInitScript(lang => localStorage.setItem("xirang.language", lang), language);
  await page.goto(`${runtime.frontendURL}/login?redirect=${encodeURIComponent(redirect)}`);
  await page.locator("#username").fill(identity.username);
  await page.locator("#password").fill(identity.password);
  const response = page.waitForResponse(response => new URL(response.url()).pathname === "/api/v1/auth/login" && response.request().method() === "POST");
  await page.locator("form button[type=submit]").click();
  const result = await response;
  expect(result.status()).toBe(200);
  expect((result.headers()["cache-control"] ?? "").includes("no-store")).toBe(true);
  expect(result.headers()["set-cookie"] === undefined).toBe(true);
  const envelope = await result.json() as { data: Enrollment | Login };
  return envelope.data;
}

function restrictedTraffic(page: Page): { business: string[]; external: string[]; verifyCount: () => number } {
  const business: string[] = [];
  const external: string[] = [];
  let verification = 0;
  page.on("request", request => {
    const url = new URL(request.url());
    if (url.pathname === verifyPath) verification++;
    if (url.pathname.startsWith("/api/v1/") && !["/api/v1/auth/captcha", "/api/v1/auth/login", verifyPath, "/api/v1/version"].includes(url.pathname)) business.push(url.pathname);
    if (url.protocol.startsWith("http") && url.hostname !== "127.0.0.1" && url.hostname !== "localhost") external.push(url.hostname);
  });
  return { business, external, verifyCount: () => verification };
}

async function assertNoSession(page: Page, privateValues: string[] = []): Promise<void> {
  const values = await page.evaluate(() => ({ session: { ...sessionStorage }, local: { ...localStorage } }));
  expect(Object.keys(values.session).some(key => /token|auth|user/i.test(key))).toBe(false);
  const serialized = JSON.stringify(values);
  expect(privateValues.some(value => value !== "" && serialized.includes(value))).toBe(false);
  expect(new URL(page.url()).pathname).toBe("/login");
}

async function recoveryEnrollment(page: Page, runtime: RecoveryRuntime, identity: Pick<Identity, "username" | "password">, language = "en"): Promise<Enrollment> {
  const result = await browserPassword(page, runtime, identity, language);
  runtime.stage = "browser-enrollment";
  expect("requires_reenrollment" in result && result.requires_reenrollment).toBe(true);
  const enrollment = result as Enrollment;
  expect(Object.keys(enrollment).sort()).toEqual(["enrollment", "recovery_token", "requires_reenrollment"]);
  await expect(page.getByTestId("recovery-enrollment-panel")).toBeVisible();
  await expect(page.getByTestId("recovery-enrollment-qr")).toBeVisible();
  expect((await page.getByTestId("recovery-enrollment-secret").textContent()) === enrollment.enrollment.secret).toBe(true);
  expect(enrollment.enrollment.qr_url.startsWith("otpauth://totp/")).toBe(true);
  expect(new URL(enrollment.enrollment.qr_url).searchParams.get("secret") === enrollment.enrollment.secret).toBe(true);
  expect(new Date(enrollment.enrollment.expires_at).getTime() > Date.now()).toBe(true);
  await assertNoSession(page, [enrollment.recovery_token, enrollment.enrollment.secret]);
  return enrollment;
}

async function submitRecovery(page: Page, secret: string): Promise<string[]> {
  await page.locator("#recovery-enrollment-code").fill(await freshAuthenticatorCode(secret));
  const response = page.waitForResponse(response => new URL(response.url()).pathname === verifyPath && response.request().method() === "POST");
  await page.getByTestId("recovery-enrollment-verify").click();
  const result = await response;
  expect(result.status()).toBe(200);
  expect(result.headers()["cache-control"].includes("no-store")).toBe(true);
  expect(result.headers()["set-cookie"] === undefined).toBe(true);
  const envelope = await result.json() as { data: { recovery_codes: string[] } };
  expect(Object.keys(envelope.data)).toEqual(["recovery_codes"]);
  const codes = envelope.data.recovery_codes;
  expect(codes.length).toBe(8);
  expect(new Set(codes).size).toBe(8);
  expect(codes.every(code => typeof code === "string" && code.length > 0)).toBe(true);
  await expect(page.getByTestId("recovery-enrollment-code-item")).toHaveCount(8);
  const displayed = await page.getByTestId("recovery-enrollment-code-item").allTextContents();
  expect(displayed.every((code, index) => code === codes[index])).toBe(true);
  await expect(page.getByTestId("recovery-enrollment-panel")).toHaveCount(0);
  await expect(page.getByTestId("recovery-enrollment-return")).toBeDisabled();
  await assertNoSession(page, codes);
  return codes;
}

async function returnAfterSaving(page: Page): Promise<void> {
  await page.locator("#recovery-enrollment-saved").check();
  await page.getByTestId("recovery-enrollment-return").click();
  await expect(page.locator("#password")).toBeVisible();
  await expect(page.getByTestId("recovery-codes-panel")).toHaveCount(0);
}

async function sanitizedScreenshot(page: Page, info: TestInfo, name: string): Promise<void> {
  // Explicit evidence is redacted at capture, never capture then edit the file.
  await page.screenshot({ path: info.outputPath(name), fullPage: true, mask: [page.getByTestId("recovery-enrollment-qr"), page.getByTestId("recovery-enrollment-secret"), page.locator("#recovery-enrollment-code"), page.getByTestId("recovery-enrollment-codes"), page.locator("#password"), page.locator("#totp-code")] });
}

for (const { role, language, viewport } of [
  { role: "admin", language: "en", viewport: { width: 1440, height: 900 } },
  { role: "admin", language: "zh", viewport: { width: 1440, height: 900 } },
  { role: "operator", language: "en", viewport: { width: 390, height: 844 } },
  { role: "operator", language: "zh", viewport: { width: 390, height: 844 } },
] as const) {
  test(`factor-only ${role} ${language} ${viewport.width}x${viewport.height}: offline reset preserves identity and resources, restricted enrollment never installs a session`, async ({ recovery, page }, info) => {
    const identity = await seed(recovery, role);
    const before = state(recovery, identity.username);
    const ownership = recovery.sql("SELECT node_id,user_id FROM node_owners WHERE node_id=? ORDER BY user_id", [identity.nodeId]);
    const nodeBefore = recovery.sql("SELECT id,name,host,port,backup_dir,tags FROM nodes WHERE id=?", [identity.nodeId]);
    await offlineReset(recovery, identity);
    const reset = state(recovery, identity.username);
    expect(reset.id).toBe(identity.id);
    expect(reset.role).toBe(role);
    expect(reset.password_hash === before.password_hash).toBe(true);
    expect(reset.token_version).toBe(before.token_version + 1);
    expect(reset.totp_enabled).toBe(0);
    expect(reset.totp_reenrollment_required).toBe(1);
    expect(reset.totp_secret === "" && reset.recovery_codes === "").toBe(true);
    expect(recovery.sql<{ n: number }>("SELECT COUNT(*) n FROM auth_pending_tokens WHERE user_id=?", [identity.id])[0].n).toBe(0);
    expect((await recovery.request("/me", undefined, identity.token)).status).toBe(401);
    expect((await recovery.request("/auth/2fa/login", { login_token: identity.pending, totp_code: await freshAuthenticatorCode(identity.secret) })).status).toBe(401);
    const revoked = recovery.sql<{ status: string; revoked_at: string | null }>("SELECT status,revoked_at FROM credential_access_grants WHERE id=?", [identity.grantId])[0];
    expect(revoked.status).toBe("revoked");
    expect(Boolean(revoked.revoked_at)).toBe(true);

    await page.setViewportSize(viewport);
    const traffic = restrictedTraffic(page);
    const enrollment = await recoveryEnrollment(page, recovery, identity, language);
    await expect(page.getByRole("heading", { name: language === "en" ? "Bind a new authenticator" : "绑定新的验证器", exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.locator("#recovery-enrollment-code").focus();
    await expect(page.locator("#recovery-enrollment-code")).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(page.getByTestId("recovery-enrollment-verify")).toBeFocused();
    await sanitizedScreenshot(page, info, `${role}-restricted-redacted.png`);
    // Optional bounded headed inspection: no inspector, credentials file or
    // retained runtime. The same owned fixture resumes and cleans up afterward.
    const inspectMilliseconds = Number(process.env.R19_RECOVERY_INSPECT_MS ?? "0");
    if (inspectMilliseconds > 0 && role === "admin" && language === "en") {
      if (!Number.isInteger(inspectMilliseconds) || inspectMilliseconds > 300_000) throw new Error("Inspection duration must be 1..300000 milliseconds");
      test.setTimeout(180_000 + inspectMilliseconds);
      await new Promise<void>(resolve => { setTimeout(resolve, inspectMilliseconds); });
    }
    const codes = await submitRecovery(page, enrollment.enrollment.secret);
    expect(traffic.verifyCount()).toBe(1);
    expect(traffic.business).toEqual([]);
    expect(traffic.external).toEqual([]);
    await assertNoSession(page, [enrollment.recovery_token, enrollment.enrollment.secret, ...codes]);
    await sanitizedScreenshot(page, info, `${role}-codes-redacted.png`);
    expect((await recovery.request("/auth/recovery/enrollment/verify", { code: await freshAuthenticatorCode(enrollment.enrollment.secret) }, enrollment.recovery_token)).status).toBe(401);
    await returnAfterSaving(page);
    await page.locator("#username").fill(identity.username);
    await page.locator("#password").fill(identity.password);
    await page.locator("form button[type=submit]").click();
    await expect(page.locator("#totp-code")).toBeVisible();
    await page.locator("#totp-code").fill(await freshAuthenticatorCode(enrollment.enrollment.secret));
    await page.locator("form button[type=submit]").click();
    await expect(page).toHaveURL(/\/app\/nodes$/);
    const session = await recovery.normalLogin(identity.username, identity.password, enrollment.enrollment.secret);
    const me = await recovery.request<{ user: { id: number; role: string } }>("/me", undefined, session.token);
    expect(me.status).toBe(200);
    expect(me.data.user.id).toBe(identity.id);
    expect(me.data.user.role).toBe(role);
    expect(recovery.sql("SELECT node_id,user_id FROM node_owners WHERE node_id=? ORDER BY user_id", [identity.nodeId])).toEqual(ownership);
    expect(recovery.sql("SELECT id,name,host,port,backup_dir,tags FROM nodes WHERE id=?", [identity.nodeId])).toEqual(nodeBefore);
    expect((await recovery.request(`/nodes/${identity.nodeId}`, undefined, session.token)).status).toBe(200);
    const oldCodePending = await recovery.login(identity.username, identity.password);
    expect((await recovery.request("/auth/2fa/login", { login_token: oldCodePending.login_token, totp_code: identity.codes[0] })).status).toBe(401);
    // Use a code genuinely valid for the old factor, but not one of the new
    // factor's allowed adjacent windows (avoid accidental six-digit collision).
    const now = Date.now();
    const newWindowCodes = [-30_000, 0, 30_000].map(offset => authenticatorCode(enrollment.enrollment.secret, now + offset));
    const oldCode = [0, -30_000, 30_000].map(offset => authenticatorCode(identity.secret, now + offset)).find(code => !newWindowCodes.includes(code));
    if (!oldCode) throw new Error("Synthetic factor codes collided across every window");
    const oldSecretPending = await recovery.login(identity.username, identity.password);
    expect((await recovery.request("/auth/2fa/login", { login_token: oldSecretPending.login_token, totp_code: oldCode })).status).toBe(401);
    const staleProofUse = await recovery.request(role === "admin" ? "/credential-access-grants/config-export" : "/credential-access-grants/batch-command", { reason: "Synthetic stale proof refusal", node_ids: [identity.nodeId] }, session.token, "POST", { "X-Xirang-Step-Up": identity.proof });
    expect(staleProofUse.status).toBe(403);
    expect(recovery.sql<{ status: string }>("SELECT status FROM credential_access_grants WHERE id=?", [identity.grantId])[0].status).toBe("revoked");
    if (role === "admin") {
      const newProof = await recovery.request<{ proof: string }>("/auth/step-up", { code: await freshAuthenticatorCode(enrollment.enrollment.secret), step_up_action: "config.export" }, session.token);
      expect(newProof.status).toBe(200);
      const protectedUse = await recovery.request<{ error_code: string }>("/config/export?include_secrets=true", undefined, session.token, "GET", { "X-Xirang-Step-Up": newProof.data.proof });
      expect(protectedUse.status).toBe(403);
      expect(protectedUse.data.error_code).toBe("CREDENTIAL_GRANT_REQUIRED");
    }
    const completed = state(recovery, identity.username);
    expect(completed.totp_enabled).toBe(1);
    expect(completed.totp_reenrollment_required).toBe(0);
    expect(completed.token_version).toBe(before.token_version + 2);
    expect(recovery.sql<{ n: number }>("SELECT COUNT(*) n FROM account_recovery_audits WHERE target_user_id=? AND event='reenrollment_completed'", [identity.id])[0].n).toBe(1);
  });
}

for (const mode of ["password-only", "combined"] as const) {
  test(`${mode}: real controlling-terminal reset applies only the chosen credential scope`, async ({ recovery, page }) => {
    const identity = await seed(recovery);
    const before = state(recovery, identity.username);
    const replacement = syntheticPassword();
    let currentSecret = identity.secret;
    await offlineReset(recovery, identity, true, mode === "combined", replacement);
    const reset = state(recovery, identity.username);
    expect(reset.password_hash !== before.password_hash).toBe(true);
    expect(reset.id).toBe(before.id);
    expect(reset.role).toBe(before.role);
    expect((await recovery.request("/auth/login", { username: identity.username, password: identity.password })).status).toBe(401);
    expect((await recovery.request("/me", undefined, identity.token)).status).toBe(401);
    expect((await recovery.request("/auth/2fa/login", { login_token: identity.pending, totp_code: await freshAuthenticatorCode(identity.secret) })).status).toBe(401);
    expect(recovery.sql<{ status: string }>("SELECT status FROM credential_access_grants WHERE id=?", [identity.grantId])[0].status).toBe("revoked");
    if (mode === "password-only") {
      expect(reset.totp_enabled).toBe(1);
      expect(reset.totp_reenrollment_required).toBe(0);
      expect(reset.totp_secret === before.totp_secret && reset.recovery_codes === before.recovery_codes).toBe(true);
      const session = await recovery.normalLogin(identity.username, replacement, identity.secret);
      expect((await recovery.request("/me", undefined, session.token)).status).toBe(200);
      const pending = await recovery.login(identity.username, replacement);
      expect((await recovery.request("/auth/2fa/login", { login_token: pending.login_token, totp_code: identity.codes[0] })).status).toBe(200);
    } else {
      const traffic = restrictedTraffic(page);
      const enrollment = await recoveryEnrollment(page, recovery, { ...identity, password: replacement });
      currentSecret = enrollment.enrollment.secret;
      await submitRecovery(page, enrollment.enrollment.secret);
      expect(traffic.business).toEqual([]);
      await returnAfterSaving(page);
      const session = await recovery.normalLogin(identity.username, replacement, enrollment.enrollment.secret);
      expect((await recovery.request("/me", undefined, session.token)).status).toBe(200);
    }
    await browserPassword(page, recovery, { ...identity, password: replacement });
    await expect(page.locator("#totp-code")).toBeVisible();
    await expect(page.getByTestId("recovery-enrollment-panel")).toHaveCount(0);
    await page.locator("#totp-code").fill(await freshAuthenticatorCode(currentSecret));
    await page.locator("form button[type=submit]").click();
    await expect(page).toHaveURL(/\/app\/nodes$/);
    expect(recovery.sql<{ n: number }>("SELECT COUNT(*) n FROM node_owners WHERE node_id=? AND user_id=?", [identity.nodeId, identity.id])[0].n).toBe(1);
  });
}

test("CLI help, absent database admission, redirected password input, wrong confirmation and read-only status", async ({ recovery }) => {
  const identity = await seed(recovery);
  await recovery.stop();
  expect(recovery.cliObservation(["-h"], true)).toMatchObject({ exit: 0, help: true, noDatabaseCreated: true });
  expect(recovery.cliObservation(["-username", "admin", "-reason", "Synthetic no create", "-reset-totp", "-offline-confirmed"], true)).toMatchObject({ exit: 1, noDatabaseCreated: true });
  const before = state(recovery, identity.username);
  const redirected = recovery.cli(identity.username, true, false, syntheticPassword(), false, true);
  expect(redirected.exit).toBe(1);
  expect(redirected.terminalRejected).toBe(true);
  expect(redirected.prompts).toBe(0);
  const wrong = recovery.cli(identity.username, false, true, undefined, true);
  expect(wrong.exit).toBe(1);
  expect(wrong.rejected).toBe(true);
  expect(state(recovery, identity.username).token_version).toBe(before.token_version);
  expect(recovery.cliObservation(["-status", "0".repeat(32)])).toMatchObject({ exit: 0, notFound: true });
  const success = recovery.cli(identity.username, false, true);
  expect(success.committed).toBe(true);
  expect(recovery.cliObservation(["-status", success.operationId!])).toMatchObject({ exit: 0, committed: true });
});

test("real HTTP rejects wrong passwords, formats, codes, purposes, expired and replaced recovery tokens", async ({ recovery, page }) => {
  const identity = await seed(recovery);
  await offlineReset(recovery, identity);
  const before = state(recovery, identity.username);
  expect((await recovery.request("/auth/login", { username: identity.username, password: syntheticPassword() })).status).toBe(401);
  expect(state(recovery, identity.username).totp_enrollment_id === before.totp_enrollment_id).toBe(true);
  const traffic = restrictedTraffic(page);
  const first = await recoveryEnrollment(page, recovery, identity);
  for (const token of [identity.token, identity.pending, identity.proof, recovery.expiredToken(first.recovery_token)]) {
    const invalid = await recovery.request<{ error_code: string }>("/auth/recovery/enrollment/verify", { code: await freshAuthenticatorCode(first.enrollment.secret) }, token);
    expect(invalid.status).toBe(401);
    expect(invalid.noStore).toBe(true);
    expect(invalid.data.error_code).toBe("RECOVERY_ENROLLMENT_INVALID");
  }
  for (const body of [{ code: "bad" }, { code: 123456 }, { code: "123456", extra: true }]) {
    expect((await recovery.request("/auth/recovery/enrollment/verify", body, first.recovery_token)).status).toBe(400);
  }
  for (const [route, body] of [["/me", undefined], ["/auth/2fa/setup", {}], ["/auth/2fa/verify", { code: "123456", enrollment_id: first.enrollment.enrollment_id }], ["/auth/step-up", { code: "123456", step_up_action: "config.export" }], ["/credential-access-grants/config-export", { reason: "Synthetic restricted denial" }]] as const) {
    expect((await recovery.request(route, body, first.recovery_token)).status).toBe(401);
  }
  await freshAuthenticatorCode(first.enrollment.secret);
  const validWindowCodes = [-30_000, 0, 30_000].map(offset => authenticatorCode(first.enrollment.secret, Date.now() + offset));
  const wrong = ["000000", "000001", "000002", "000003"].find(code => !validWindowCodes.includes(code))!;
  await page.locator("#recovery-enrollment-code").fill(wrong);
  const wrongResponse = page.waitForResponse(response => new URL(response.url()).pathname === verifyPath);
  await page.getByTestId("recovery-enrollment-verify").click();
  const rejected = await wrongResponse;
  expect(rejected.status()).toBe(403);
  const error = await rejected.json() as { data: { error_code: string } };
  expect(error.data.error_code).toBe("TOTP_CODE_INVALID");
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page.getByTestId("recovery-enrollment-panel")).toBeVisible();
  // Cancel disposes local secrets; next password login replaces server enrollment.
  await page.getByTestId("recovery-enrollment-back").click();
  await assertNoSession(page, [first.recovery_token, first.enrollment.secret]);
  const second = await recoveryEnrollment(page, recovery, identity);
  expect(second.enrollment.enrollment_id !== first.enrollment.enrollment_id).toBe(true);
  expect(second.enrollment.secret !== first.enrollment.secret).toBe(true);
  expect((await recovery.request("/auth/recovery/enrollment/verify", { code: await freshAuthenticatorCode(first.enrollment.secret) }, first.recovery_token)).status).toBe(401);
  await submitRecovery(page, second.enrollment.secret);
  expect(traffic.business).toEqual([]);
});

test("committed completion response loss is unknown, returns to password and is never replayed", async ({ recovery, page }) => {
  const identity = await seed(recovery);
  await offlineReset(recovery, identity);
  const traffic = restrictedTraffic(page);
  const enrollment = await recoveryEnrollment(page, recovery, identity);
  let committedStatus = 0;
  await page.route(`**${verifyPath}`, async route => {
    const response = await route.fetch(); // Real server commits before only the transport is lost.
    committedStatus = response.status();
    await route.abort("connectionreset");
  });
  await page.locator("#recovery-enrollment-code").fill(await freshAuthenticatorCode(enrollment.enrollment.secret));
  await page.getByTestId("recovery-enrollment-verify").click();
  await expect(page.locator("#password")).toBeVisible();
  await expect(page.getByTestId("recovery-enrollment-notice")).toBeVisible();
  expect(committedStatus).toBe(200);
  expect(traffic.verifyCount()).toBe(1);
  expect(traffic.business).toEqual([]);
  await assertNoSession(page, [enrollment.recovery_token, enrollment.enrollment.secret]);
  expect(state(recovery, identity.username).totp_reenrollment_required).toBe(0);
  expect(recovery.sql<{ n: number }>("SELECT COUNT(*) n FROM account_recovery_audits WHERE target_user_id=? AND event='reenrollment_completed'", [identity.id])[0].n).toBe(1);
  expect((await recovery.request("/auth/recovery/enrollment/verify", { code: await freshAuthenticatorCode(enrollment.enrollment.secret) }, enrollment.recovery_token)).status).toBe(401);
  await page.unroute(`**${verifyPath}`);
  const session = await recovery.normalLogin(identity.username, identity.password, enrollment.enrollment.secret);
  // The lost batch is not recovered/re-shown. Existing authenticated regeneration
  // creates a different batch only after a fresh normal login.
  const regenerated = await recovery.request<{ recovery_codes: string[] }>("/auth/2fa/recovery-codes/regenerate", { code: await freshAuthenticatorCode(enrollment.enrollment.secret) }, session.token);
  expect(regenerated.status).toBe(200);
  expect(regenerated.data.recovery_codes.length).toBe(8);
  expect(traffic.verifyCount()).toBe(1);
});

test("copy denial retains codes; refresh discards unsaved codes and never restores a restricted token", async ({ recovery, page }) => {
  const identity = await seed(recovery);
  await offlineReset(recovery, identity);
  const enrollment = await recoveryEnrollment(page, recovery, identity);
  await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => { throw new Error("synthetic clipboard denial"); } } }));
  const codes = await submitRecovery(page, enrollment.enrollment.secret);
  await page.getByTestId("recovery-enrollment-copy").click();
  await expect(page.getByTestId("recovery-enrollment-code-item")).toHaveCount(8);
  await expect(page.getByRole("alert")).toBeVisible();
  await assertNoSession(page, codes);
  await page.reload();
  await expect(page.locator("#password")).toBeVisible();
  await expect(page.getByTestId("recovery-codes-panel")).toHaveCount(0);
  await assertNoSession(page, [enrollment.recovery_token, enrollment.enrollment.secret, ...codes]);
});

test("expiry clears only local secrets without resubmission and next login creates a fresh enrollment", async ({ recovery, page }) => {
  const identity = await seed(recovery);
  await offlineReset(recovery, identity);
  await page.clock.install();
  const traffic = restrictedTraffic(page);
  const enrollment = await recoveryEnrollment(page, recovery, identity);
  await page.clock.fastForward(11 * 60_000);
  await expect(page.locator("#password")).toBeVisible();
  await expect(page.getByTestId("recovery-enrollment-notice")).toBeVisible();
  await expect(page.getByTestId("recovery-enrollment-panel")).toHaveCount(0);
  expect(traffic.verifyCount()).toBe(0);
  expect(traffic.business).toEqual([]);
  await assertNoSession(page, [enrollment.recovery_token, enrollment.enrollment.secret]);
  // Browser monotonic clock is advanced, not production wall time or DB state.
  await page.clock.resume();
  const replacement = await recovery.request<Enrollment>("/auth/login", { username: identity.username, password: identity.password });
  expect(replacement.status).toBe(200);
  expect(replacement.data.enrollment.enrollment_id !== enrollment.enrollment.enrollment_id).toBe(true);
});

test("live ordinary, pending and step-up tokens cannot complete another account's recovery; realtime rejects recovery purpose", async ({ recovery, page }) => {
  const identity = await seed(recovery);
  const otherPassword = syntheticPassword();
  expect((await recovery.request("/users", { username: "unaffected-operator", password: otherPassword, role: "operator" }, identity.token)).status).toBe(201);
  const other = await recovery.activate("unaffected-operator", otherPassword);
  const otherProof = await recovery.request<{ proof: string }>("/auth/step-up", { code: await freshAuthenticatorCode(other.secret), step_up_action: "batch_command.create" }, other.token);
  expect(otherProof.status).toBe(200);
  const otherPending = await recovery.login("unaffected-operator", otherPassword);
  await offlineReset(recovery, identity);
  expect((await recovery.request("/me", undefined, other.token)).status).toBe(200);
  const enrollment = await recoveryEnrollment(page, recovery, identity);
  for (const token of [other.token, otherPending.login_token, otherProof.data.proof]) {
    const result = await recovery.request<{ error_code: string }>("/auth/recovery/enrollment/verify", { code: await freshAuthenticatorCode(enrollment.enrollment.secret) }, token);
    expect(result.status).toBe(401);
    expect(result.data.error_code).toBe("RECOVERY_ENROLLMENT_INVALID");
    expect(result.noStore).toBe(true);
    expect(result.cookie).toBe(false);
  }
  // These are raw invalid JSON contracts, not mocked server responses.
  for (const rawBody of ['{"code":"123456","code":"123456"}', '{"code":"123456"}{}', '[]', '{"code":"123456","padding":"' + "x".repeat(1100) + '"}']) {
    const response = await fetch(recovery.backendURL + verifyPath, { method: "POST", headers: { Authorization: `Bearer ${enrollment.recovery_token}`, "Content-Type": "application/json" }, body: rawBody });
    expect([400, 413].includes(response.status)).toBe(true);
    expect((response.headers.get("cache-control") ?? "").includes("no-store")).toBe(true);
  }
  const closeCode = await page.evaluate(async ({ origin, token }) => {
    return new Promise<number>(resolve => {
      const socket = new WebSocket(origin.replace("http:", "ws:") + "/api/v1/ws/logs");
      const timer = setTimeout(() => { socket.close(); resolve(0); }, 10_000);
      socket.onopen = () => socket.send(JSON.stringify({ type: "auth", token }));
      socket.onclose = event => { clearTimeout(timer); resolve(event.code); };
      socket.onerror = () => { clearTimeout(timer); resolve(0); };
    });
  }, { origin: recovery.backendURL, token: enrollment.recovery_token });
  expect(closeCode).toBe(1008);
  expect(state(recovery, identity.username).totp_reenrollment_required).toBe(1);
  expect(state(recovery, "unaffected-operator").totp_reenrollment_required).toBe(0);
  await assertNoSession(page, [enrollment.recovery_token, enrollment.enrollment.secret]);
});

test("a wrong new-authenticator code can be corrected in the same restricted enrollment without a password replay", async ({ recovery, page }) => {
  const identity = await seed(recovery);
  await offlineReset(recovery, identity);
  const traffic = restrictedTraffic(page);
  const enrollment = await recoveryEnrollment(page, recovery, identity);
  await freshAuthenticatorCode(enrollment.enrollment.secret);
  const validCodes = [-30_000, 0, 30_000].map(offset => authenticatorCode(enrollment.enrollment.secret, Date.now() + offset));
  const wrong = ["000000", "000001", "000002", "000003"].find(code => !validCodes.includes(code))!;
  await page.locator("#recovery-enrollment-code").fill(wrong);
  const response = page.waitForResponse(response => new URL(response.url()).pathname === verifyPath);
  await page.getByTestId("recovery-enrollment-verify").click();
  expect((await response).status()).toBe(403);
  await expect(page.getByRole("alert")).toBeVisible();
  expect(state(recovery, identity.username).totp_enrollment_id === enrollment.enrollment.enrollment_id).toBe(true);
  await submitRecovery(page, enrollment.enrollment.secret);
  expect(traffic.verifyCount()).toBe(2);
  expect(traffic.business).toEqual([]);
  expect(recovery.sql<{ n: number }>("SELECT COUNT(*) n FROM account_recovery_audits WHERE target_user_id=? AND event='reenrollment_completed'", [identity.id])[0].n).toBe(1);
});
