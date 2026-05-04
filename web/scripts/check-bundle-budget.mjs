#!/usr/bin/env node

import { readdir, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// 主 JS 预算调涨原因（reference: .trellis/tasks/05-04-wave-1-wave-0-out-of-scope-b-2-b-8-f-3/prd.md F-3）：
// PR-A 引入 @tanstack/react-virtual v3（gzipped 5.4 KiB，含 virtual-core）用于 logs-viewer
// 列表虚拟化，预算从 540 → 546 KiB，留 ~6 KiB 缓冲覆盖该依赖与未来微调。
const DEFAULT_MAIN_JS_BUDGET_KIB = 546;
const DEFAULT_MAIN_CSS_BUDGET_KIB = 70;

function formatKiB(bytes) {
  return `${(bytes / 1024).toFixed(2)} KiB`;
}

function readBudgetBytes(envName, defaultBudgetKiB) {
  const rawValue = process.env[envName];
  if (!rawValue) {
    return defaultBudgetKiB * 1024;
  }

  const parsed = Number(rawValue);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`环境变量 ${envName} 必须是正数，当前值为: ${rawValue}`);
  }

  return Math.round(parsed * 1024);
}

async function pickMainAsset(assetsDir, extension) {
  const allFiles = await readdir(assetsDir);
  const candidateFiles = allFiles.filter((file) => file.endsWith(extension));
  if (candidateFiles.length === 0) {
    throw new Error(`未找到 ${extension} 产物，请先执行前端构建`);
  }

  const indexFiles = candidateFiles.filter((file) => file.startsWith('index-'));
  const pool = indexFiles.length > 0 ? indexFiles : candidateFiles;

  let selectedFile = '';
  let selectedSize = -1;
  for (const file of pool) {
    const fullPath = path.join(assetsDir, file);
    const fileSize = (await stat(fullPath)).size;
    if (fileSize > selectedSize) {
      selectedFile = file;
      selectedSize = fileSize;
    }
  }

  return { file: selectedFile, size: selectedSize };
}

function printResult(label, file, sizeBytes, budgetBytes) {
  const passed = sizeBytes <= budgetBytes;
  const status = passed ? 'OK' : '超预算';
  console.log(
    `[bundle-budget] ${label}: ${file} | 当前 ${formatKiB(sizeBytes)} | 预算 ${formatKiB(
      budgetBytes,
    )} | ${status}`,
  );
  return passed;
}

async function main() {
  const scriptDir = path.dirname(fileURLToPath(import.meta.url));
  const webDir = path.resolve(scriptDir, '..');
  const assetsDir = path.join(webDir, 'dist', 'assets');

  const mainJsBudgetBytes = readBudgetBytes('BUNDLE_BUDGET_MAIN_JS_KIB', DEFAULT_MAIN_JS_BUDGET_KIB);
  const mainCssBudgetBytes = readBudgetBytes(
    'BUNDLE_BUDGET_MAIN_CSS_KIB',
    DEFAULT_MAIN_CSS_BUDGET_KIB,
  );

  const [mainJs, mainCss] = await Promise.all([
    pickMainAsset(assetsDir, '.js'),
    pickMainAsset(assetsDir, '.css'),
  ]);

  console.log(`[bundle-budget] 检查目录: ${assetsDir}`);
  const jsPassed = printResult('主 JS', mainJs.file, mainJs.size, mainJsBudgetBytes);
  const cssPassed = printResult('主 CSS', mainCss.file, mainCss.size, mainCssBudgetBytes);

  if (!jsPassed || !cssPassed) {
    console.error('[bundle-budget] 预算检查失败，请优化打包体积后重试。');
    process.exit(1);
  }

  console.log('[bundle-budget] 预算检查通过。');
}

main().catch((error) => {
  console.error(`[bundle-budget] 执行失败: ${error.message}`);
  process.exit(1);
});
