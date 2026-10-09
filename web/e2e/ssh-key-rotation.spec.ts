import { execFileSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { chmodSync, lstatSync, mkdtempSync, readFileSync, realpathSync, rmSync } from "node:fs";
import { createServer, type Socket } from "node:net";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { expect, test, type APIResponse, type Locator, type Page } from "@playwright/test";
import { e2eAdminPassword, loginAs } from "./real-backend-session";

const COPY = {
  notSaved: "未替换，原密钥保持不变。",
  validationFailed: "候选连接未全部通过。",
  estimate: "预估数量",
  inventoryHint: "这里只是页面缓存的预估数量。确认后，服务端会按完整库存验证并决定是否保存。",
  returnToEdit: "返回修改并重新检查",
  verified: "验证通过",
  failed: "验证失败",
  connectionFailed: "连接失败",
  savedOutcome: "全部受影响节点已验证通过。",
  fingerprintLabel: "新公钥指纹:",
  checkCandidate: "检查候选密钥",
  confirm: "确认轮换",
  next: "下一步",
  dialog: "密钥轮换",
  savedToast: "密钥已更新",
  editDialog: "编辑 SSH Key",
  updateKey: "更新密钥",
  keyUpdated: "SSH Key 已更新。",
  actions: "操作",
  rotate: "轮换密钥",
  edit: "编辑",
  search: "搜索 SSH 密钥",
} as const;

type RuntimeFixture = {
  runtimeReal: string;
  port: number;
  username: string;
  sqlitePath: string;
  privateKeyPath: string;
  knownHostsPath: string;
};
type StoredKey = {
  id: number;
  name: string;
  username: string;
  keyType: string;
  privateKey: string;
  fingerprint: string;
  disabled: number;
  expiresAt: string | null;
  allowedPurposes: string;
  allowedNodeIds: string;
  allowedNodeTags: string;
  lastUsedAt: string | null;
  updatedAt: string;
};
type StoredNode = {
  id: number;
  name: string;
  host: string;
  port: number;
  username: string;
  authType: string;
  sshKeyId: number;
  tags: string;
  archived: number;
  status: string;
};
type KeyMaterial = { privateKey: string; publicFingerprint: string };
type RefusedPort = { port: number; connections: () => number; close: () => Promise<void> };
type WriteCounts = { put: number; rotate: number; test: number };

test.describe("ssh key rotation", () => {
  test("keeps the stored key when one node cannot be reached, then saves the candidate after that node is repaired and checked again", async ({ page }, testInfo) => {
    test.setTimeout(240_000);
    const token = scenarioToken(testInfo.testId);
    const names = {
      key: `e2e-rot-key-${token}`,
      up: `e2e-rot-up-${token}`,
      down: `e2e-rot-down-${token}`,
      digest: `e2e-rot-digest-${token}`,
    };
    const problems: string[] = [];
    const fixture = runtimeFixture();
    let refused: RefusedPort | null = null;
    let keyDir = "";
    let user = "";
    try {
      const closed = await openRefusedPort(400);
      refused = closed;
      keyDir = createKeyDirectory(fixture.runtimeReal);
      if (closed.port === fixture.port) throw new Error("closed port collided with the isolated sshd");
      const knownHosts = readFileSync(fixture.knownHostsPath, "utf8");
      expect(knownHosts).toContain(`[127.0.0.1]:${fixture.port} `);
      const candidate = readKeyMaterial(fixture.privateKeyPath);
      const oldKey = generateKey(keyDir, "old");
      expect(oldKey.publicFingerprint).not.toBe(candidate.publicFingerprint);
      user = await beginAdmin(page, testInfo.testId);
      const candidateDigest = await probePrivateDigest(page, fixture.sqlitePath, names.digest, candidate.privateKey, fixture.username);
      const keyId = await createKey(page, names.key, fixture.username, oldKey.privateKey);
      const before = storedKey(fixture.sqlitePath, names.key);
      expect(before.id).toBe(keyId);
      expect(before.fingerprint).not.toBe(candidateDigest);
      const upId = await createBoundNode(page, names.up, keyId, fixture.username, fixture.port);
      const downId = await createBoundNode(page, names.down, keyId, fixture.username, closed.port);
      retargetNode(fixture.sqlitePath, upId, names.up, fixture.port);
      retargetNode(fixture.sqlitePath, downId, names.down, closed.port);
      const upBefore = storedNode(fixture.sqlitePath, names.up);
      const downBefore = storedNode(fixture.sqlitePath, names.down);
      expect(upBefore).toMatchObject({ host: "127.0.0.1", port: fixture.port, sshKeyId: keyId, authType: "key", archived: 0 });
      expect(downBefore).toMatchObject({ host: "127.0.0.1", port: closed.port, sshKeyId: keyId, authType: "key", archived: 0 });

      const writes = trackKeyWrites(page, keyId);
      await openKeyAction(page, names.key, COPY.rotate);
      const dialog = page.getByRole("dialog", { name: COPY.dialog });
      await expect(dialog).toBeVisible();

      await test.step("leave the original key in place when the second node fails", async () => {
        await checkCandidate(page, dialog, candidate.privateKey);
        const outcome = await confirmRotation(page, dialog, keyId, "2");
        expect(outcome.status).toBe("not_saved");
        expect(outcome.reason).toBe("validation_failed");
        const summary = dialog.locator("[data-testid='rotation-summary']");
        await expect(summary).toHaveAttribute("data-rotation-status", "not_saved");
        await expect(summary).toHaveAttribute("data-rotation-reason", "validation_failed");
        await expect(dialog.getByText(COPY.notSaved, { exact: true })).toBeVisible();
        await expect(dialog.getByText(COPY.validationFailed, { exact: true })).toBeVisible();
        await expectNodeStatus(dialog, names.up, upId, "verified");
        await expectNodeStatus(dialog, names.down, downId, "failed");
        await expect(dialog.locator("[data-testid='rotation-saved-fingerprint']")).toHaveCount(0);
        await expect(page.locator("[data-rotation-draft]")).toHaveAttribute("data-rotation-draft", "present");
        await expect(dialog.getByRole("button", { name: COPY.returnToEdit, exact: true })).toBeVisible();
        await expect(dialog.getByRole("link", { name: "打开密钥页", exact: true })).toHaveCount(0);
        await expect(dialog.getByRole("link", { name: "打开节点页", exact: true })).toHaveCount(0);
        await expect(dialog.getByRole("button", { name: /重新验证|Re-verify/ })).toHaveCount(0);
        await expect(page.getByText(COPY.savedToast, { exact: true })).toHaveCount(0);
        await expect(dialog).not.toContainText(/PRIVATE KEY|connection_failed|connection refused/i);
        expect(closed.connections()).toBeGreaterThan(0);
        expect(writes.snapshot()).toEqual({ put: 0, rotate: 1, test: 0 });
        assertMaterialUnchanged(before, storedKey(fixture.sqlitePath, names.key));
        expect(storedNode(fixture.sqlitePath, names.up)).toEqual(upBefore);
        expect(storedNode(fixture.sqlitePath, names.down)).toEqual(downBefore);
      });

      await test.step("save only after a fresh check of the repaired inventory", async () => {
        const refusedBeforeRepair = closed.connections();
        await dialog.getByRole("button", { name: COPY.returnToEdit, exact: true }).click();
        const field = dialog.locator("#rotation-private-key");
        await expect(field).toBeVisible();
        expect((await field.inputValue()).replace(/\s+/g, "")).toBe(candidate.privateKey.replace(/\s+/g, ""));
        await expect(page.locator("[data-rotation-draft]")).toHaveAttribute("data-rotation-draft", "present");
        await expect(page.locator("[data-rotation-candidate]")).toHaveAttribute("data-rotation-candidate", "absent");
        await expect(dialog.getByRole("button", { name: COPY.next, exact: true })).toBeDisabled();
        repairNodePort(fixture.sqlitePath, downId, names.down, closed.port, fixture.port);
        const repaired = storedNode(fixture.sqlitePath, names.down);
        expect(repaired.port).toBe(fixture.port);
        await checkCandidate(page, dialog, candidate.privateKey);
        const outcome = await confirmRotation(page, dialog, keyId, "2");
        expect(outcome.status).toBe("saved");
        expect(outcome.reason).toBe("");
        expect(outcome.fingerprint).toBe(candidate.publicFingerprint);
        const summary = dialog.locator("[data-testid='rotation-summary']");
        await expect(summary).toHaveAttribute("data-rotation-status", "saved");
        await expect(summary).toHaveAttribute("data-rotation-reason", "");
        await expect(dialog.locator("[data-testid='rotation-saved-fingerprint']")).toHaveText(candidate.publicFingerprint);
        await expect(dialog.getByText(COPY.fingerprintLabel, { exact: true })).toBeVisible();
        await expect(dialog.getByText(COPY.savedOutcome, { exact: true })).toBeVisible();
        await expect(dialog.getByText(COPY.notSaved, { exact: true })).toHaveCount(0);
        await expectNodeStatus(dialog, names.up, upId, "verified");
        await expectNodeStatus(dialog, names.down, downId, "verified");
        await expect(page.locator("[data-rotation-draft]")).toHaveAttribute("data-rotation-draft", "cleared");
        await expect(page.getByText(COPY.savedToast, { exact: true })).toBeVisible();
        expect(closed.connections()).toBe(refusedBeforeRepair);
        expect(writes.snapshot()).toEqual({ put: 0, rotate: 2, test: 0 });
        assertCandidateStored(before, storedKey(fixture.sqlitePath, names.key), candidateDigest);
        expect(storedNode(fixture.sqlitePath, names.up)).toEqual(upBefore);
        expect(storedNode(fixture.sqlitePath, names.down)).toEqual(repaired);
      });
    } finally {
      if (refused) await closeQuietly(refused, problems);
      if (keyDir !== "") removeKeyDirectory(keyDir, fixture.runtimeReal, problems);
      deleteScenario(fixture.sqlitePath, {
        user,
        keys: [names.key, names.digest],
        nodes: [names.up, names.down],
      }, problems);
      if (problems.length > 0) {
        test.info().annotations.push({ type: "cleanup", description: problems.join("; ").slice(0, 300) });
      }
    }
    if (problems.length > 0) throw new Error(`cleanup failed: ${problems.join("; ")}`);
  });

  test("saves an ordinary key edit while its node is offline", async ({ page }, testInfo) => {
    test.setTimeout(180_000);
    const token = scenarioToken(`${testInfo.testId}:edit`);
    const names = {
      key: `e2e-rot-edit-${token}`,
      node: `e2e-rot-off-${token}`,
      digest: `e2e-rot-edig-${token}`,
    };
    const problems: string[] = [];
    const fixture = runtimeFixture();
    let refused: RefusedPort | null = null;
    let keyDir = "";
    let user = "";
    try {
      const closed = await openRefusedPort(0);
      refused = closed;
      keyDir = createKeyDirectory(fixture.runtimeReal);
      if (closed.port === fixture.port) throw new Error("closed port collided with the isolated sshd");
      const current = generateKey(keyDir, "current");
      const replacement = generateKey(keyDir, "replacement");
      expect(replacement.publicFingerprint).not.toBe(current.publicFingerprint);
      user = await beginAdmin(page, `${testInfo.testId}:edit`);
      const replacementDigest = await probePrivateDigest(page, fixture.sqlitePath, names.digest, replacement.privateKey, fixture.username);
      const keyId = await createKey(page, names.key, fixture.username, current.privateKey);
      const nodeId = await createBoundNode(page, names.node, keyId, fixture.username, closed.port);
      retargetNode(fixture.sqlitePath, nodeId, names.node, closed.port);
      const before = storedKey(fixture.sqlitePath, names.key);
      const nodeBefore = storedNode(fixture.sqlitePath, names.node);
      expect(before.fingerprint).not.toBe(replacementDigest);
      expect(nodeBefore).toMatchObject({ host: "127.0.0.1", port: closed.port, sshKeyId: keyId, authType: "key" });
      const writes = trackKeyWrites(page, keyId);
      await openKeyAction(page, names.key, COPY.edit);
      const dialog = page.getByRole("dialog", { name: COPY.editDialog });
      await expect(dialog).toBeVisible();
      const field = dialog.locator("#ssh-key-edit-private-key");
      await expect(field).toHaveValue("");
      await field.fill(replacement.privateKey);
      const pending = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return response.request().method() === "PUT" && url.pathname === `/api/v1/ssh-keys/${keyId}`;
      }, { timeout: 20_000 });
      await dialog.getByRole("button", { name: COPY.updateKey, exact: true }).click();
      const response = await pending;
      expect(response.status()).toBe(200);
      await expect(page.getByText(COPY.keyUpdated, { exact: true })).toBeVisible();
      await expect(dialog).toBeHidden();
      expect(closed.connections()).toBe(0);
      expect(writes.snapshot()).toEqual({ put: 1, rotate: 0, test: 0 });
      assertCandidateStored(before, storedKey(fixture.sqlitePath, names.key), replacementDigest);
      expect(storedNode(fixture.sqlitePath, names.node)).toEqual(nodeBefore);
    } finally {
      if (refused) await closeQuietly(refused, problems);
      if (keyDir !== "") removeKeyDirectory(keyDir, fixture.runtimeReal, problems);
      deleteScenario(fixture.sqlitePath, {
        user,
        keys: [names.key, names.digest],
        nodes: [names.node],
      }, problems);
      if (problems.length > 0) {
        test.info().annotations.push({ type: "cleanup", description: problems.join("; ").slice(0, 300) });
      }
    }
    if (problems.length > 0) throw new Error(`cleanup failed: ${problems.join("; ")}`);
  });
});

async function beginAdmin(page: Page, seed: string): Promise<string> {
  await page.setViewportSize({ width: 1440, height: 900 });
  await isolateClient(page, seed);
  const username = `e2e-rot-admin-${scenarioToken(seed)}`;
  insertIndependentUser(runtimeFixture().sqlitePath, username);
  await loginAs(page, username, e2eAdminPassword());
  // loginAs pins English on every later document. Register Chinese after it so the last init script wins.
  await page.addInitScript(() => localStorage.setItem("xirang.language", "zh"));
  const toChinese = page.getByRole("button", { name: "切换到中文", exact: true });
  await expect(toChinese).toBeVisible();
  await toChinese.click();
  await expect(page.getByRole("button", { name: "Switch to English", exact: true })).toBeVisible();
  return username;
}

async function openKeyAction(page: Page, keyName: string, action: string): Promise<void> {
  const nodesLoaded = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === "GET" && url.pathname === "/api/v1/nodes" && response.ok();
  });
  const keysLoaded = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === "GET" && url.pathname === "/api/v1/ssh-keys" && response.ok();
  });
  await page.goto("/app/ssh-keys");
  await nodesLoaded;
  await keysLoaded;
  await page.getByRole("textbox", { name: COPY.search, exact: true }).fill(keyName);
  const row = page.getByRole("row").filter({ hasText: keyName });
  await row.getByRole("button", { name: COPY.actions, exact: true }).click();
  await page.getByRole("menuitem", { name: action, exact: true }).click();
}

async function checkCandidate(page: Page, dialog: Locator, privateKey: string): Promise<void> {
  const field = dialog.locator("#rotation-private-key");
  await expect(field).toBeVisible();
  if ((await field.inputValue()) !== privateKey) await field.fill(privateKey);
  const preview = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === "POST" && url.pathname === "/api/v1/ssh-keys/preview";
  }, { timeout: 20_000 });
  await dialog.getByRole("button", { name: COPY.checkCandidate, exact: true }).click();
  expect((await preview).ok()).toBe(true);
  const next = dialog.getByRole("button", { name: COPY.next, exact: true });
  await expect(next).toBeEnabled();
  await next.click();
  await expect(dialog.locator("#ssh-key-rotation-ack")).toBeVisible();
}

async function confirmRotation(
  page: Page,
  dialog: Locator,
  keyId: number,
  expectedCount: string,
): Promise<{ status: string; reason: string; fingerprint: string }> {
  await expect(dialog.getByText(COPY.estimate, { exact: true })).toBeVisible();
  await expect(dialog.getByText(COPY.inventoryHint, { exact: true })).toBeVisible();
  const label = dialog.locator("label[for='ssh-key-rotation-ack']");
  await expect(label).toHaveText(`输入 ${expectedCount} 以确认预估节点数`);
  await dialog.locator("#ssh-key-rotation-ack").fill(expectedCount);
  const confirm = dialog.getByRole("button", { name: COPY.confirm, exact: true });
  await expect(confirm).toBeEnabled();
  const pending = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === "POST" && url.pathname === `/api/v1/ssh-keys/${keyId}/rotate`;
  }, { timeout: 30_000 });
  await confirm.click();
  const response = await pending;
  expect(response.status()).toBe(200);
  return rotationPayload(response);
}

async function expectNodeStatus(
  dialog: Locator,
  name: string,
  nodeId: number,
  status: "verified" | "failed",
): Promise<void> {
  const row = dialog.locator("[data-testid='rotation-node-result']").filter({ hasText: name });
  await expect(row).toHaveAttribute("data-node-id", `node-${nodeId}`);
  await expect(row).toHaveAttribute("data-node-status", status);
  await expect(row).toContainText(status === "verified" ? COPY.verified : COPY.failed);
  if (status === "failed") await expect(row).toContainText(COPY.connectionFailed);
  if (status === "verified") {
    await expect(row).not.toContainText(COPY.failed);
    await expect(row).not.toContainText(COPY.connectionFailed);
  }
}

function trackKeyWrites(page: Page, keyId: number): { snapshot: () => WriteCounts } {
  const counts: WriteCounts = { put: 0, rotate: 0, test: 0 };
  page.on("request", (request) => {
    let pathname = "";
    try {
      pathname = new URL(request.url()).pathname;
    } catch {
      return;
    }
    if (request.method() === "PUT" && pathname === `/api/v1/ssh-keys/${keyId}`) counts.put += 1;
    if (request.method() === "POST" && pathname === `/api/v1/ssh-keys/${keyId}/rotate`) counts.rotate += 1;
    if (request.method() === "POST" && pathname === `/api/v1/ssh-keys/${keyId}/test-connection`) counts.test += 1;
  });
  return { snapshot: () => ({ ...counts }) };
}

async function rotationPayload(response: APIResponse): Promise<{ status: string; reason: string; fingerprint: string }> {
  let body: unknown;
  try {
    body = await response.json();
  } catch {
    throw new Error("rotation response was not json");
  }
  const data = body && typeof body === "object" && "data" in body ? (body as { data?: unknown }).data : null;
  if (!data || typeof data !== "object") throw new Error("rotation response has no data");
  const row = data as { status?: unknown; reason?: unknown; public_key_fingerprint?: unknown };
  if (row.status !== "saved" && row.status !== "not_saved") throw new Error("rotation response status is invalid");
  if (typeof row.reason !== "string") throw new Error("rotation response reason is invalid");
  if (typeof row.public_key_fingerprint !== "string") throw new Error("rotation response fingerprint is invalid");
  return { status: row.status, reason: row.reason, fingerprint: row.public_key_fingerprint };
}

async function probePrivateDigest(
  page: Page,
  dbPath: string,
  name: string,
  privateKey: string,
  username: string,
): Promise<string> {
  const id = await createKey(page, name, username, privateKey);
  const digest = storedKey(dbPath, name).fingerprint;
  if (!digest.startsWith("SHA256:")) throw new Error("probe private digest was not stored");
  const removed = await submitJson(page, "DELETE", `/api/v1/ssh-keys/${id}`);
  if (removed.status < 200 || removed.status >= 300) {
    throw new Error(`probe key delete failed (${removed.status})`);
  }
  if (sqliteRows(dbPath, `SELECT id FROM ssh_keys WHERE name = '${sqlText(name)}';`).length !== 0) {
    throw new Error("probe key remained in the database");
  }
  return digest;
}

async function createKey(page: Page, name: string, username: string, privateKey: string): Promise<number> {
  const created = await submitJson(page, "POST", "/api/v1/ssh-keys", {
    name,
    username,
    key_type: "ed25519",
    private_key: privateKey,
  });
  if (created.status !== 201 || created.id === null) {
    throw new Error(`ssh key create failed (${created.status}): ${created.message}`);
  }
  return created.id;
}

async function createBoundNode(page: Page, name: string, keyId: number, username: string, port: number): Promise<number> {
  const created = await submitJson(page, "POST", "/api/v1/nodes", {
    name,
    host: "e2e-ssh.invalid",
    port,
    username,
    auth_type: "key",
    ssh_key_id: keyId,
    tags: "",
    base_path: "/",
    use_sudo: false,
  });
  if (created.status !== 201 || created.id === null || created.host !== "e2e-ssh.invalid") {
    throw new Error(`node create failed (${created.status}): ${created.message}`);
  }
  return created.id;
}

async function submitJson(
  page: Page,
  method: "POST" | "DELETE",
  apiPath: string,
  body?: unknown,
): Promise<{ status: number; id: number | null; host: string | null; message: string }> {
  return page.evaluate(async (request: { method: "POST" | "DELETE"; apiPath: string; body?: unknown }) => {
    const token = sessionStorage.getItem("xirang-auth-token");
    const response = await fetch(request.apiPath, {
      method: request.method,
      headers: {
        Authorization: `Bearer ${token ?? ""}`,
        ...(request.body === undefined ? {} : { "Content-Type": "application/json" }),
      },
      ...(request.body === undefined ? {} : { body: JSON.stringify(request.body) }),
    });
    let id: number | null = null;
    let host: string | null = null;
    let message = "";
    try {
      const payload = await response.json() as { message?: unknown; data?: { id?: unknown; host?: unknown } };
      message = typeof payload.message === "string" ? payload.message : "";
      id = typeof payload.data?.id === "number" && Number.isInteger(payload.data.id) ? payload.data.id : null;
      host = typeof payload.data?.host === "string" ? payload.data.host : null;
    } catch {
      message = "unreadable response";
    }
    if (message.includes("PRIVATE")) message = "rejected";
    return { status: response.status, id, host, message: message.slice(0, 160) };
  }, { method, apiPath, body });
}

function assertMaterialUnchanged(before: StoredKey, after: StoredKey): void {
  if (before.privateKey !== after.privateKey) throw new Error(`stored private key for ${before.name} changed`);
  expect(publicKeyFields(after)).toEqual(publicKeyFields(before));
}

function assertCandidateStored(before: StoredKey, after: StoredKey, privateDigest: string): void {
  if (after.privateKey === before.privateKey) throw new Error(`stored private key for ${before.name} was not replaced`);
  expect(after.fingerprint).toBe(privateDigest);
  expect(after.fingerprint).not.toBe(before.fingerprint);
  expect(after.id).toBe(before.id);
  expect(after.name).toBe(before.name);
  expect(after.username).toBe(before.username);
  expect(after.keyType).toBe(before.keyType);
  expect(after.disabled).toBe(before.disabled);
  expect(after.expiresAt).toBe(before.expiresAt);
  expect(after.allowedPurposes).toBe(before.allowedPurposes);
  expect(after.allowedNodeIds).toBe(before.allowedNodeIds);
  expect(after.allowedNodeTags).toBe(before.allowedNodeTags);
  expect(after.lastUsedAt).toBe(before.lastUsedAt);
}

function publicKeyFields(key: StoredKey): Omit<StoredKey, "privateKey"> {
  return {
    id: key.id,
    name: key.name,
    username: key.username,
    keyType: key.keyType,
    fingerprint: key.fingerprint,
    disabled: key.disabled,
    expiresAt: key.expiresAt,
    allowedPurposes: key.allowedPurposes,
    allowedNodeIds: key.allowedNodeIds,
    allowedNodeTags: key.allowedNodeTags,
    lastUsedAt: key.lastUsedAt,
    updatedAt: key.updatedAt,
  };
}

async function isolateClient(page: Page, seed: string): Promise<void> {
  const client = createHash("sha256").update(seed).digest("hex");
  await page.setExtraHTTPHeaders({ "X-Forwarded-For": `2001:db8:${client.slice(0, 4)}:${client.slice(4, 8)}::2` });
}

function scenarioToken(seed: string): string {
  return createHash("sha256").update(`${seed}:${randomBytes(4).toString("hex")}`).digest("hex").slice(0, 8);
}

function generateKey(directory: string, label: "old" | "current" | "replacement"): KeyMaterial {
  if (!/^(old|current|replacement)$/.test(label)) throw new Error("refusing unexpected key label");
  const filePath = path.join(directory, label);
  execFileSync("ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-C", `xirang-e2e-rotation-${label}`, "-f", filePath], {
    timeout: 15_000,
    stdio: ["ignore", "pipe", "pipe"],
  });
  chmodSync(filePath, 0o600);
  return readKeyMaterial(filePath);
}

function readKeyMaterial(filePath: string): KeyMaterial {
  let privateKey: string;
  try {
    privateKey = readFileSync(filePath, "utf8");
  } catch (error) {
    throw new Error(`isolated private key could not be read (${errorCode(error)})`);
  }
  if (!privateKey.includes("PRIVATE KEY") || privateKey.includes("\0") || privateKey.includes("ENCRYPTED")) {
    throw new Error("isolated private key is not an unencrypted private key");
  }
  const output = execFileSync("ssh-keygen", ["-lf", filePath], { encoding: "utf8", timeout: 10_000, stdio: ["ignore", "pipe", "pipe"] });
  const match = output.match(/SHA256:[A-Za-z0-9+/]+/);
  if (!match) throw new Error("public fingerprint was not produced");
  return { privateKey: privateKey.endsWith("\n") ? privateKey : `${privateKey}\n`, publicFingerprint: match[0] };
}

function createKeyDirectory(runtimeReal: string): string {
  const directory = mkdtempSync(path.join(runtimeReal, "rotation-keys-"));
  chmodSync(directory, 0o700);
  return directory;
}

function openRefusedPort(holdMs: number): Promise<RefusedPort> {
  return new Promise((resolve, reject) => {
    let connections = 0;
    const sockets = new Set<Socket>();
    const server = createServer((socket) => {
      connections += 1;
      sockets.add(socket);
      const timer = setTimeout(() => socket.destroy(), holdMs);
      socket.on("close", () => {
        clearTimeout(timer);
        sockets.delete(socket);
      });
    });
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        reject(new Error("closed port was not allocated"));
        return;
      }
      resolve({
        port: address.port,
        connections: () => connections,
        close: () => new Promise((done, fail) => {
          for (const socket of sockets) socket.destroy();
          server.close((error) => {
            if (error) fail(error);
            else done();
          });
        }),
      });
    });
  });
}

async function closeQuietly(port: RefusedPort, problems: string[]): Promise<void> {
  try {
    await port.close();
  } catch (error) {
    problems.push(error instanceof Error ? error.message.slice(0, 160) : "closed port cleanup failed");
  }
}

function removeKeyDirectory(directory: string, runtimeReal: string, problems: string[]): void {
  try {
    const resolved = realpathSync(directory);
    const relative = path.relative(runtimeReal, resolved);
    if (!path.basename(resolved).startsWith("rotation-keys-") || relative === "" || relative === ".." || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
      throw new Error("refusing to delete a key directory outside the runtime");
    }
    rmSync(resolved, { recursive: true, force: true });
  } catch (error) {
    problems.push(error instanceof Error ? error.message.slice(0, 160) : "key directory cleanup failed");
  }
}

function deleteScenario(
  dbPath: string,
  scenario: { user: string; keys: string[]; nodes: string[] },
  problems: string[],
): void {
  try {
    for (const name of scenario.nodes) {
      const safe = sqlText(name);
      try {
        sqliteRows(dbPath, `DELETE FROM node_owners WHERE node_id IN (SELECT id FROM nodes WHERE name = '${safe}');`);
      } catch (error) {
        const message = error instanceof Error ? error.message : "";
        if (!message.includes("no such table")) throw error;
      }
      sqliteRows(dbPath, `DELETE FROM nodes WHERE name = '${safe}';`);
    }
    for (const name of scenario.keys) {
      sqliteRows(dbPath, `DELETE FROM ssh_keys WHERE name = '${sqlText(name)}';`);
    }
    if (scenario.user !== "") {
      const username = sqlUser(scenario.user);
      sqliteRows(dbPath, `DELETE FROM users WHERE username = '${username}' AND role = 'admin' AND totp_enabled = 0;`);
    }
  } catch (error) {
    problems.push(error instanceof Error ? error.message.slice(0, 160) : "scenario cleanup failed");
  }
}

function retargetNode(dbPath: string, nodeId: number, nodeName: string, port: number): void {
  assertNodeIdentity(nodeId, nodeName, port);
  const rows = sqliteRows(dbPath, [
    `UPDATE nodes SET host = '127.0.0.1' WHERE id = ${nodeId} AND name = '${sqlText(nodeName)}' AND host = 'e2e-ssh.invalid' AND port = ${port};`,
    `SELECT changes() AS changes, host, port FROM nodes WHERE id = ${nodeId} AND name = '${sqlText(nodeName)}';`,
  ].join("\n"));
  const row = rows[0];
  if (rows.length !== 1 || row === undefined || row.changes !== 1 || row.host !== "127.0.0.1" || row.port !== port) {
    throw new Error("node host update did not retarget exactly one row");
  }
}

function repairNodePort(dbPath: string, nodeId: number, nodeName: string, closedPort: number, openPort: number): void {
  assertNodeIdentity(nodeId, nodeName, closedPort);
  assertNodeIdentity(nodeId, nodeName, openPort);
  const rows = sqliteRows(dbPath, [
    `UPDATE nodes SET port = ${openPort} WHERE id = ${nodeId} AND name = '${sqlText(nodeName)}' AND host = '127.0.0.1' AND port = ${closedPort};`,
    `SELECT changes() AS changes, host, port FROM nodes WHERE id = ${nodeId} AND name = '${sqlText(nodeName)}';`,
  ].join("\n"));
  const row = rows[0];
  if (rows.length !== 1 || row === undefined || row.changes !== 1 || row.host !== "127.0.0.1" || row.port !== openPort) {
    throw new Error("node port repair did not update exactly one row");
  }
}

function assertNodeIdentity(nodeId: number, nodeName: string, port: number): void {
  if (!Number.isInteger(nodeId) || nodeId < 1) throw new Error("refusing invalid node id");
  sqlText(nodeName);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("refusing invalid node port");
}

function storedKey(dbPath: string, name: string): StoredKey {
  const rows = sqliteRows(dbPath, [
    "SELECT id, name, username, key_type, private_key, fingerprint, disabled, expires_at,",
    "allowed_purposes, allowed_node_ids, allowed_node_tags, last_used_at, updated_at",
    `FROM ssh_keys WHERE name = '${sqlText(name)}';`,
  ].join(" "));
  if (rows.length !== 1 || rows[0] === undefined) throw new Error(`stored ssh key ${name} was not found`);
  const row = rows[0];
  const id = requireInt(row.id, "stored ssh key id");
  const disabled = requireInt(row.disabled, "stored ssh key disabled");
  if (id < 1 || (disabled !== 0 && disabled !== 1)) throw new Error("stored ssh key row is invalid");
  const privateKey = requireText(row.private_key, "stored private key");
  if (privateKey === "") throw new Error("stored private key is empty");
  return {
    id,
    name: requireText(row.name, "stored ssh key name"),
    username: requireText(row.username, "stored ssh key username"),
    keyType: requireText(row.key_type, "stored ssh key type"),
    privateKey,
    fingerprint: requireText(row.fingerprint, "stored ssh key fingerprint"),
    disabled,
    expiresAt: requireNullableText(row.expires_at, "stored ssh key expiry"),
    allowedPurposes: requireText(row.allowed_purposes, "stored ssh key purposes"),
    allowedNodeIds: requireText(row.allowed_node_ids, "stored ssh key node ids"),
    allowedNodeTags: requireText(row.allowed_node_tags, "stored ssh key node tags"),
    lastUsedAt: requireNullableText(row.last_used_at, "stored ssh key last used"),
    updatedAt: requireText(row.updated_at, "stored ssh key updated at"),
  };
}

function storedNode(dbPath: string, name: string): StoredNode {
  const rows = sqliteRows(dbPath, [
    "SELECT id, name, host, port, username, auth_type, ssh_key_id, tags, archived, status",
    `FROM nodes WHERE name = '${sqlText(name)}';`,
  ].join(" "));
  if (rows.length !== 1 || rows[0] === undefined) throw new Error(`stored node ${name} was not found`);
  const row = rows[0];
  const id = requireInt(row.id, "stored node id");
  const port = requireInt(row.port, "stored node port");
  const sshKeyId = requireInt(row.ssh_key_id, "stored node ssh key");
  const archived = requireInt(row.archived, "stored node archived");
  if (id < 1 || port < 1 || port > 65535 || sshKeyId < 1 || (archived !== 0 && archived !== 1)) {
    throw new Error("stored node row is invalid");
  }
  return {
    id,
    name: requireText(row.name, "stored node name"),
    host: requireText(row.host, "stored node host"),
    port,
    username: requireText(row.username, "stored node username"),
    authType: requireText(row.auth_type, "stored node auth"),
    sshKeyId,
    tags: requireText(row.tags, "stored node tags"),
    archived,
    status: requireText(row.status, "stored node status"),
  };
}

function insertIndependentUser(dbPath: string, username: string): void {
  const safe = sqlUser(username);
  const rows = sqliteRows(dbPath, [
    "INSERT INTO users (",
    "username, password_hash, role, totp_secret, totp_enabled, recovery_codes,",
    "token_version, totp_enrollment_id, totp_enrollment_expires_at, onboarded,",
    "created_at, updated_at",
    ") SELECT",
    `'${safe}',`,
    "(SELECT password_hash FROM users WHERE username = 'admin'),",
    "'admin', '', 0, '', 0, '', NULL, 1, datetime('now'), datetime('now')",
    "WHERE (SELECT COUNT(*) FROM users WHERE username = 'admin') = 1",
    `AND (SELECT COUNT(*) FROM users WHERE username = '${safe}') = 0;`,
    "SELECT changes() AS changes,",
    `(SELECT role FROM users WHERE username = '${safe}') AS role,`,
    `(SELECT totp_enabled FROM users WHERE username = '${safe}') AS totp_enabled;`,
  ].join("\n"));
  const row = rows[0];
  if (rows.length !== 1 || row === undefined || row.changes !== 1 || row.role !== "admin" || row.totp_enabled !== 0) {
    throw new Error("independent rotation admin was not inserted");
  }
}

function runtimeFixture(): RuntimeFixture {
  const runtimeDir = (process.env.E2E_RUNTIME_DIR ?? "").trim();
  if (!path.isAbsolute(runtimeDir)) throw new Error("E2E_RUNTIME_DIR must be absolute");
  const scratch = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../.tmp/agent");
  let scratchInfo;
  try {
    scratchInfo = lstatSync(scratch);
  } catch (error) {
    throw new Error(`.tmp/agent is not available for the SSH fixture (${errorCode(error)})`);
  }
  if (scratchInfo.isSymbolicLink() || !scratchInfo.isDirectory()) throw new Error(".tmp/agent must be a real directory");
  assertNoSymlinkThrough(runtimeDir, path.dirname(scratch));
  let runtimeInfo;
  try {
    runtimeInfo = lstatSync(runtimeDir);
  } catch (error) {
    throw new Error(`E2E_RUNTIME_DIR is not an existing directory (${errorCode(error)})`);
  }
  if (runtimeInfo.isSymbolicLink() || !runtimeInfo.isDirectory()) throw new Error("E2E_RUNTIME_DIR must be a real directory");
  const scratchReal = realpathSync(scratch);
  const runtimeReal = realpathSync(runtimeDir);
  const relativeRuntime = path.relative(scratchReal, runtimeReal);
  if (relativeRuntime === "" || relativeRuntime === ".." || relativeRuntime.startsWith(`..${path.sep}`) || path.isAbsolute(relativeRuntime)) {
    throw new Error("E2E_RUNTIME_DIR must be a subdirectory of .tmp/agent");
  }
  const fixturePath = path.join(runtimeReal, "fixture.json");
  let fixtureInfo;
  try {
    fixtureInfo = lstatSync(fixturePath);
  } catch (error) {
    throw new Error(`isolated SSH fixture is missing (${errorCode(error)})`);
  }
  if (fixtureInfo.isSymbolicLink() || !fixtureInfo.isFile()) throw new Error("isolated SSH fixture must be a regular file");
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(fixturePath, "utf8"));
  } catch {
    throw new Error("isolated SSH fixture is not json");
  }
  if (!parsed || typeof parsed !== "object") throw new Error("isolated SSH fixture is not an object");
  const record = parsed as { port?: unknown; username?: unknown; privateKeyPath?: unknown; sqlitePath?: unknown; knownHostsPath?: unknown };
  if (typeof record.port !== "number" || !Number.isInteger(record.port) || record.port < 1 || record.port > 65535) {
    throw new Error("isolated SSH fixture port is invalid");
  }
  if (typeof record.username !== "string" || !/^[a-z_][a-z0-9_-]{0,31}$/.test(record.username)) {
    throw new Error("isolated SSH fixture username is invalid");
  }
  return {
    runtimeReal,
    port: record.port,
    username: record.username,
    privateKeyPath: fixtureFile(record.privateKeyPath, runtimeReal),
    sqlitePath: fixtureFile(record.sqlitePath, runtimeReal),
    knownHostsPath: fixtureFile(record.knownHostsPath, runtimeReal),
  };
}

function fixtureFile(value: unknown, runtimeReal: string): string {
  if (typeof value !== "string" || !path.isAbsolute(value)) throw new Error("isolated runtime fixture path is invalid");
  const info = lstatSync(value);
  if (info.isSymbolicLink() || !info.isFile()) throw new Error("isolated runtime fixture path must be a regular file");
  const resolved = realpathSync(value);
  const relative = path.relative(runtimeReal, resolved);
  if (relative === "" || relative === ".." || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
    throw new Error("isolated runtime fixture path escaped the runtime");
  }
  return resolved;
}

function assertNoSymlinkThrough(leaf: string, boundary: string): void {
  let current = path.resolve(leaf);
  const stop = path.resolve(boundary);
  for (;;) {
    const info = lstatSync(current);
    if (info.isSymbolicLink()) throw new Error(`${current} must not be a symlink`);
    if (current === stop) return;
    const parent = path.dirname(current);
    if (parent === current) throw new Error(`${leaf} is outside ${boundary}`);
    current = parent;
  }
}

function sqliteRows(dbPath: string, sql: string): Record<string, unknown>[] {
  const uri = pathToFileURL(path.resolve(dbPath));
  uri.searchParams.set("mode", "rw");
  let output = "";
  try {
    output = execFileSync("sqlite3", ["-json", "-batch", "-bail", "-init", "/dev/null", "-cmd", ".timeout 5000", uri.href, sql], {
      encoding: "utf8",
      timeout: 10_000,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : "sqlite3 failed";
    throw new Error(message.replace(/-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----/g, "[redacted-private-key]").slice(0, 400));
  }
  const trimmed = output.trim();
  if (trimmed === "") return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    throw new Error("sqlite json result is not parseable");
  }
  if (!Array.isArray(parsed)) throw new Error("sqlite json result is not an array");
  return parsed.map((row) => {
    if (!row || typeof row !== "object" || Array.isArray(row)) throw new Error("sqlite json row is not an object");
    return row as Record<string, unknown>;
  });
}

function requireInt(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isInteger(value)) throw new Error(`${label} is invalid`);
  return value;
}

function requireText(value: unknown, label: string): string {
  if (typeof value !== "string") throw new Error(`${label} is invalid`);
  return value;
}

function requireNullableText(value: unknown, label: string): string | null {
  if (value === null) return null;
  return requireText(value, label);
}

function sqlText(value: string): string {
  if (!/^e2e-rot-[a-z0-9-]+$/.test(value)) throw new Error("refusing unexpected sql text");
  return value;
}

function sqlUser(value: string): string {
  if (!/^e2e-rot-admin-[0-9a-f]{8}$/.test(value)) throw new Error("refusing unexpected independent username");
  return value;
}

function errorCode(error: unknown): string {
  return error && typeof error === "object" && "code" in error ? String(error.code) : "unknown";
}
