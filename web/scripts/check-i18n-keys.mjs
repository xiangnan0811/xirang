#!/usr/bin/env node
/**
 * i18n key 对齐检查脚本
 * 检测 zh.ts 和 en.ts 之间缺失或多余的翻译 key
 *
 * 用法: node scripts/check-i18n-keys.mjs
 */

import { readFileSync } from "fs";
import { resolve, dirname } from "path";
import { fileURLToPath } from "url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const root = resolve(__dirname, "../src/i18n/locales");

function loadLocale(name) {
  const raw = readFileSync(resolve(root, `${name}.ts`), "utf-8");
  const trimmed = raw
    .replace(/^const \w+ = /, "")
    .replace(/\s*as\s+const\s*;\s*export default \w+;\s*$/, "");
  return new Function(`return (${trimmed})`)();
}

function flattenKeys(obj, prefix = "") {
  const keys = new Set();
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k;
    if (v !== null && typeof v === "object" && !Array.isArray(v)) {
      for (const sub of flattenKeys(v, path)) {
        keys.add(sub);
      }
    } else {
      keys.add(path);
    }
  }
  return keys;
}

const zh = loadLocale("zh");
const en = loadLocale("en");

const zhKeys = flattenKeys(zh);
const enKeys = flattenKeys(en);

const missingInEn = [...zhKeys].filter((k) => !enKeys.has(k)).sort();
const missingInZh = [...enKeys].filter((k) => !zhKeys.has(k)).sort();

let exitCode = 0;

if (missingInEn.length > 0) {
  console.error(`\n[ERROR] ${missingInEn.length} key(s) in zh.ts but missing in en.ts:`);
  for (const k of missingInEn) console.error(`  - ${k}`);
  exitCode = 1;
}

if (missingInZh.length > 0) {
  console.error(`\n[ERROR] ${missingInZh.length} key(s) in en.ts but missing in zh.ts:`);
  for (const k of missingInZh) console.error(`  - ${k}`);
  exitCode = 1;
}

if (exitCode === 0) {
  console.log(`i18n keys aligned: ${zhKeys.size} keys in both zh.ts and en.ts`);
}

process.exit(exitCode);
