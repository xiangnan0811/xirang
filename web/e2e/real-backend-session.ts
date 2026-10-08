import { createHmac } from "node:crypto";
import { expect, type Page } from "@playwright/test";

export function e2eAdminPassword(): string {
  return process.env.E2E_ADMIN_PASSWORD ?? "FAKE_E2E_AdminPass2026!_FOR_TEST_ONLY";
}

export async function loginAs(page: Page, username: string, password: string): Promise<void> {
  await page.addInitScript(() => localStorage.setItem("xirang.language", "en"));
  await page.goto("/login");
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/app\/overview$/);
  const wizard = page.getByRole("dialog").filter({ has: page.getByRole("button", { name: "Start Setup", exact: true }) });
  await expect(wizard).toBeVisible({ timeout: 5_000 });
  await page.keyboard.press("Escape");
  await expect(wizard).toBeHidden({ timeout: 5_000 });
}

export async function login(page: Page): Promise<void> {
  await loginAs(page, "admin", e2eAdminPassword());
}

export function authenticatorCode(secret: string, now = Date.now()): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const letter of secret.replace(/=+$/, "").toUpperCase()) {
    const value = alphabet.indexOf(letter);
    if (value < 0) throw new Error("Invalid test TOTP secret");
    bits += value.toString(2).padStart(5, "0");
  }
  const key = Buffer.from((bits.match(/.{8}/g) ?? []).map(byte => Number.parseInt(byte, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(now / 30_000)));
  const digest = createHmac("sha1", key).update(counter).digest();
  const offset = digest[digest.length - 1] & 15;
  return ((digest.readUInt32BE(offset) & 0x7fffffff) % 1_000_000).toString().padStart(6, "0");
}

export async function freshAuthenticatorCode(secret: string): Promise<string> {
  const remain = 30_000 - (Date.now() % 30_000);
  if (remain < 2_000) await new Promise((resolve) => setTimeout(resolve, remain + 250));
  return authenticatorCode(secret);
}

export async function submitFreshProof(page: Page, secret: string, stepUps: { count: number }): Promise<void> {
  const dialog = page.getByRole("dialog", { name: "Additional verification required" });
  await expect(dialog).toBeVisible({ timeout: 15_000 });
  const before = stepUps.count;
  await dialog.locator("#step-up-code").fill(await freshAuthenticatorCode(secret));
  await dialog.getByRole("button", { name: "Verify", exact: true }).click();
  await expect.poll(() => stepUps.count, { timeout: 15_000 }).toBe(before + 1);
  await expect(dialog).toBeHidden();
}
