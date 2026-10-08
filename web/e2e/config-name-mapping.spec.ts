import { execFileSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { lstatSync, readFileSync, realpathSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { expect, test, type APIRequestContext, type APIResponse, type Page } from "@playwright/test";
import { e2eAdminPassword, freshAuthenticatorCode, loginAs, submitFreshProof } from "./real-backend-session";

const STEP_UP_HEADER = "X-Xirang-Step-Up";

type Warning = { entity: string; index: number; code: string; name?: string };
type ImportBody = {
  created: number;
  rejected: number;
  skipped: number;
  disabledImported: number;
  warningsTruncated: number;
  warnings: Warning[];
};
type PanelCopy = {
  listName: string;
  node: string;
  key: string;
  index: (index: number) => string;
  duplicate: string;
  invalid: string;
  conflict: string;
  correctFile: string;
  title: string;
  editKey: string;
  reviewScope: string;
  enableManually: string;
  rebindNode: string;
  verifyNodes: string;
  openKeys: string;
  openNodes: string;
  rejectedTitle: string;
  warningsTitle: string;
  unresolvedScope: string;
  unresolvedNode: string;
  missingKey: string;
};
type PanelLink = { name: string; href: string };

const SSH_KEYS_HREF = "/app/ssh-keys";
const NODES_HREF = "/app/nodes";
const PANEL_EN: PanelCopy = {
  listName: "Import warnings",
  node: "Node",
  key: "SSH key",
  index: (index) => `input index ${index} (from 0)`,
  duplicate: "The name is duplicated in the file, so this item was rejected.",
  invalid: "The name field format is invalid.",
  conflict: "Clearing the name conflicts with an existing reference.",
  correctFile: "Correct the duplicated names in the import file, then import it again.",
  title: "Fix imported keys and nodes",
  editKey: "Edit the key and add its private key.",
  reviewScope: "Review purpose, target nodes or tags, and expiry.",
  enableManually: "Enable the key manually. Key rotation does not enable a disabled key.",
  rebindNode: "Edit the node and bind its SSH key again.",
  verifyNodes: "Verify the associated nodes.",
  openKeys: "Open SSH keys",
  openNodes: "Open nodes",
  rejectedTitle: "Some imported items were rejected",
  warningsTitle: "Configuration imported with warnings",
  unresolvedScope: "The source node scope cannot be mapped to this database.",
  unresolvedNode: "The node is not bound to an SSH key on this system.",
  missingKey: "No usable private key was supplied; the key is kept disabled.",
};
const PANEL_ZH: PanelCopy = {
  listName: "导入警告",
  node: "节点",
  key: "SSH 密钥",
  index: (index) => `输入序号 ${index}（从 0 起）`,
  duplicate: "文件内名称重复，该项已拒绝。",
  invalid: "名称字段格式无效。",
  conflict: "名称清空与旧引用冲突。",
  correctFile: "请修正导入文件中的重复名称后再导入。",
  title: "修复导入的密钥和节点",
  editKey: "编辑密钥并补上私钥。",
  reviewScope: "复核用途、目标节点或标签，以及到期时间。",
  enableManually: "手动启用密钥。密钥轮换不会启用已禁用的密钥。",
  rebindNode: "编辑节点并重新绑定 SSH 密钥。",
  verifyNodes: "验证关联节点。",
  openKeys: "打开 SSH 密钥",
  openNodes: "打开节点",
  rejectedTitle: "部分导入项被拒绝",
  warningsTitle: "配置已导入，但有警告",
  unresolvedScope: "来源节点范围无法映射到当前数据库。",
  unresolvedNode: "节点未绑定当前系统中的 SSH 密钥。",
  missingKey: "未提供可用私钥，密钥保持禁用。",
};
type ListedNode = { id: number; name: string; sshKeyId: number | null };
type ListedKey = { id: number; name: string; disabled: boolean; allowedNodeIds: string };
type RuntimeFiles = { sqlitePath: string; privateKeyPath: string };

test.describe("config name mapping", () => {
  test("maps a sensitive export onto new ids after the source rows are deleted", async ({ page }, testInfo) => {
    test.setTimeout(240_000);
    const session = await beginAdmin(page, testInfo.testId);
    const token = scenarioToken(testInfo.testId);
    const names = scenarioNames(token);
    const runtime = runtimeFiles();
    const privateKey = readRuntimePrivateKey(runtime.privateKeyPath);
    const source = await createBoundPair(page, names, privateKey, "10.251.2.10", `e2emap${token}`);
    await openMaintenance(page);
    const downloaded = await downloadConfig(page, session.secret, session.stepUps, true);
    const exported = parseExport(downloaded);
    assertExportedPair(exported, names, source.nodeId, source.keyId, true);
    await deletePair(page, source.nodeId, source.keyId);
    const distractor = await createBoundPair(page, distractorNames(token), privateKey, "10.251.2.11", `e2emap${token}x`);
    const before = relationSnapshot(runtime.sqlitePath);
    const imported = await importDownload(page, session.secret, session.stepUps, downloaded, names.node);
    expect(imported.created).toBeGreaterThanOrEqual(2);
    const live = await listedPair(page, names);
    expect(live.node.id).not.toBe(source.nodeId);
    expect(live.key.id).not.toBe(source.keyId);
    expect(live.node.id).not.toBe(distractor.nodeId);
    expect(live.key.id).not.toBe(distractor.keyId);
    expect(live.node.sshKeyId).toBe(live.key.id);
    expect(scopeIds(live.key.allowedNodeIds)).toEqual([String(live.node.id)]);
    expect(live.key.disabled).toBe(false);
    const stored = storedPair(runtime.sqlitePath, names);
    expect(stored.nodeId).toBe(live.node.id);
    expect(stored.sshKeyId).toBe(live.key.id);
    expect(stored.allowedNodeIds).toBe(String(live.node.id));
    expect(stored.disabled).toBe(false);
    const distractorStored = storedPair(runtime.sqlitePath, distractorNames(token));
    expect(distractorStored.nodeId).toBe(distractor.nodeId);
    expect(distractorStored.sshKeyId).toBe(distractor.keyId);
    assertPriorRowsUnchanged(before, relationSnapshot(runtime.sqlitePath));
    await expect(page.locator("#settings-panel-maintenance").getByText("Created", { exact: true })).toBeVisible();
  });

  test("shows disabled-key remediation for a secret-free export", async ({ page }, testInfo) => {
    test.setTimeout(240_000);
    const session = await beginAdmin(page, `${testInfo.testId}:plain`);
    const token = scenarioToken(`${testInfo.testId}:plain`);
    const names = scenarioNames(token);
    const runtime = runtimeFiles();
    const privateKey = readRuntimePrivateKey(runtime.privateKeyPath);
    const source = await createBoundPair(page, names, privateKey, "10.251.3.10", `e2eplain${token}`);
    await openMaintenance(page);
    const downloaded = await downloadConfig(page, session.secret, session.stepUps, false);
    expect(downloaded.includes("PRIVATE KEY")).toBe(false);
    const exported = parseExport(downloaded);
    assertExportedPair(exported, names, source.nodeId, source.keyId, false);
    await deletePair(page, source.nodeId, source.keyId);
    await createBoundPair(page, distractorNames(token), privateKey, "10.251.3.11", `e2eplain${token}x`);
    const before = relationSnapshot(runtime.sqlitePath);
    const imported = await importDownload(page, session.secret, session.stepUps, downloaded, names.node);
    expect(imported.disabledImported).toBeGreaterThanOrEqual(1);
    requireWarning(imported.warnings, "ssh_keys", "missing_private_key", names.key);
    const live = await listedPair(page, names);
    expect(live.node.sshKeyId).toBe(live.key.id);
    expect(live.node.id).not.toBe(source.nodeId);
    expect(live.key.id).not.toBe(source.keyId);
    expect(scopeIds(live.key.allowedNodeIds)).toEqual([String(live.node.id)]);
    expect(live.key.disabled).toBe(true);
    const stored = storedPair(runtime.sqlitePath, names);
    expect(stored.sshKeyId).toBe(live.key.id);
    expect(stored.disabled).toBe(true);
    assertPriorRowsUnchanged(before, relationSnapshot(runtime.sqlitePath));
    await expectDisabledGuide(page, PANEL_EN);
    await showTheme(page, "dark");
    await expect(maintenanceOutcome(page)).toContainText(PANEL_EN.missingKey);
    await showTheme(page, "light");
    await showLanguage(page, "zh");
    await expectDisabledGuide(page, PANEL_ZH);
    await expectPanelFits(page, PANEL_ZH.openKeys);
  });

  test("reports partial rejection for invalid name references", async ({ page }, testInfo) => {
    test.setTimeout(240_000);
    const session = await beginAdmin(page, `${testInfo.testId}:bad`);
    const token = scenarioToken(`${testInfo.testId}:bad`);
    const runtime = runtimeFiles();
    const privateKey = readRuntimePrivateKey(runtime.privateKeyPath);
    const goodNode = `e2e-map,good-node-${token}`;
    const goodKey = `e2e-map,good-key-${token}`;
    const duplicateNode = `e2e-map,dup-node-${token}`;
    const duplicateKey = `e2e-map,dup-key-${token}`;
    const nullScopeKey = `e2e-map,null-scope-${token}`;
    const badTypeKey = `e2e-map,bad-type-${token}`;
    const conflictKey = `e2e-map,conflict-key-${token}`;
    const nullNode = `e2e-map,null-node-${token}`;
    const typedNode = `e2e-map,typed-node-${token}`;
    const conflictNode = `e2e-map,conflict-node-${token}`;
    const node = (name: string, host: string, backupDir: string, extra: Record<string, unknown>) => ({
      name,
      host,
      port: 22,
      username: "e2e-map",
      auth_type: "key",
      backup_dir: backupDir,
      ...extra,
    });
    const key = (name: string, extra: Record<string, unknown>) => ({
      name,
      username: "e2e-map",
      key_type: "auto",
      private_key: privateKey,
      ...extra,
    });
    const payload = {
      version: "1.0",
      data: {
        nodes: [
          node(goodNode, "10.251.4.10", `e2ebad${token}g`, { ssh_key_name: goodKey }),
          node(duplicateNode, "10.251.4.11", `e2ebad${token}d1`, { ssh_key_name: goodKey }),
          node(duplicateNode, "10.251.4.12", `e2ebad${token}d2`, { ssh_key_name: goodKey }),
          node(nullNode, "10.251.4.13", `e2ebad${token}n`, { ssh_key_name: null }),
          node(typedNode, "10.251.4.14", `e2ebad${token}t`, { ssh_key_name: 4 }),
          node(conflictNode, "10.251.4.15", `e2ebad${token}c`, { ssh_key_name: "", ssh_key_id: 8 }),
        ],
        ssh_keys: [
          key(goodKey, { allowed_node_names: [goodNode] }),
          key(nullScopeKey, { allowed_node_names: null }),
          key(badTypeKey, { allowed_node_names: "not-an-array" }),
          key(conflictKey, { allowed_node_names: [], allowed_node_ids: "9" }),
          key(duplicateKey, { allowed_node_names: [] }),
          key(duplicateKey, { allowed_node_names: [] }),
        ],
      },
    };
    await openMaintenance(page);
    const before = relationSnapshot(runtime.sqlitePath);
    const imported = await importDownload(page, session.secret, session.stepUps, JSON.stringify(payload), goodNode);
    expect(imported.created).toBe(8);
    expect(imported.rejected).toBe(4);
    expect(imported.disabledImported).toBe(3);
    requireWarning(imported.warnings, "ssh_keys", "duplicate_name", duplicateKey);
    requireWarning(imported.warnings, "nodes", "duplicate_name", duplicateNode);
    requireWarning(imported.warnings, "ssh_keys", "unresolved_node_scope", nullScopeKey);
    requireWarning(imported.warnings, "ssh_keys", "invalid_reference", badTypeKey);
    requireWarning(imported.warnings, "ssh_keys", "reference_conflict", conflictKey);
    requireWarning(imported.warnings, "nodes", "unresolved_ssh_key", nullNode);
    requireWarning(imported.warnings, "nodes", "invalid_reference", typedNode);
    requireWarning(imported.warnings, "nodes", "reference_conflict", conflictNode);
    const live = await listedPair(page, { node: goodNode, key: goodKey });
    expect(live.node.sshKeyId).toBe(live.key.id);
    expect(scopeIds(live.key.allowedNodeIds)).toEqual([String(live.node.id)]);
    expect(live.key.disabled).toBe(false);
    expect(countNamed(runtime.sqlitePath, "nodes", duplicateNode)).toBe(0);
    expect(countNamed(runtime.sqlitePath, "ssh_keys", duplicateKey)).toBe(0);
    const nullScope = storedKey(runtime.sqlitePath, nullScopeKey);
    expect(nullScope.present).toBe(true);
    expect(nullScope.disabled).toBe(true);
    expect(nullScope.allowedNodeIds).toBe("");
    const badType = storedKey(runtime.sqlitePath, badTypeKey);
    expect(badType.present).toBe(true);
    expect(badType.disabled).toBe(true);
    expect(badType.allowedNodeIds).toBe("");
    const conflictStored = storedKey(runtime.sqlitePath, conflictKey);
    expect(conflictStored.present).toBe(true);
    expect(conflictStored.disabled).toBe(true);
    expect(conflictStored.allowedNodeIds).toBe("");
    const unbound = storedNode(runtime.sqlitePath, nullNode);
    expect(unbound.present).toBe(true);
    expect(unbound.sshKeyId).toBeNull();
    const typed = storedNode(runtime.sqlitePath, typedNode);
    expect(typed.present).toBe(true);
    expect(typed.sshKeyId).toBeNull();
    const conflict = storedNode(runtime.sqlitePath, conflictNode);
    expect(conflict.present).toBe(true);
    expect(conflict.sshKeyId).toBeNull();
    assertPriorRowsUnchanged(before, relationSnapshot(runtime.sqlitePath));
    const mixedLines = (copy: PanelCopy) => [
      warningLine(copy.node, copy.index(1), duplicateNode, copy.duplicate),
      warningLine(copy.node, copy.index(2), duplicateNode, copy.duplicate),
      warningLine(copy.node, copy.index(4), typedNode, copy.invalid),
      warningLine(copy.node, copy.index(5), conflictNode, copy.conflict),
      warningLine(copy.key, copy.index(2), badTypeKey, copy.invalid),
      warningLine(copy.key, copy.index(3), conflictKey, copy.conflict),
      warningLine(copy.key, copy.index(4), duplicateKey, copy.duplicate),
      warningLine(copy.key, copy.index(5), duplicateKey, copy.duplicate),
    ];
    await expectNamePanel(page, {
      copy: PANEL_EN,
      lines: mixedLines(PANEL_EN),
      present: [PANEL_EN.rejectedTitle, PANEL_EN.unresolvedScope, PANEL_EN.unresolvedNode, PANEL_EN.correctFile, ...disabledGuide(PANEL_EN), PANEL_EN.rebindNode],
      absent: [PANEL_EN.warningsTitle],
      links: bothLinks(PANEL_EN),
      absentLinks: [],
    });
    await showLanguage(page, "zh");
    await expectNamePanel(page, {
      copy: PANEL_ZH,
      lines: mixedLines(PANEL_ZH),
      present: [PANEL_ZH.rejectedTitle, PANEL_ZH.unresolvedScope, PANEL_ZH.unresolvedNode, PANEL_ZH.correctFile, ...disabledGuide(PANEL_ZH), PANEL_ZH.rebindNode],
      absent: [PANEL_ZH.warningsTitle],
      links: bothLinks(PANEL_ZH),
      absentLinks: [],
    });
    await expectPanelFits(page, PANEL_ZH.openKeys);
  });

  test("shows name-only remediation routes for duplicate, invalid, and conflicting references", async ({ page }, testInfo) => {
    test.setTimeout(300_000);
    const session = await beginAdmin(page, `${testInfo.testId}:routes`);
    const token = scenarioToken(`${testInfo.testId}:routes`);
    const runtime = runtimeFiles();
    const privateKey = readRuntimePrivateKey(runtime.privateKeyPath);
    const names = { node: `e2e-map,route-node-${token}`, key: `e2e-map,route-key-${token}` };
    const dupNode = `e2e-map,route-dup-node-${token}`;
    const dupKey = `e2e-map,route-dup-key-${token}`;
    const document = (nodes: unknown[], sshKeys: unknown[]) => JSON.stringify({ version: "1.0", data: { nodes, ssh_keys: sshKeys } });
    const duplicateNode = (name: string, host: string, backupDir: string) => ({
      name,
      host,
      port: 22,
      username: "e2e-map",
      auth_type: "key",
      backup_dir: backupDir,
      ssh_key_name: names.key,
    });
    const duplicateKey = (name: string) => ({
      name,
      username: "e2e-map",
      key_type: "auto",
      allowed_node_names: [] as string[],
    });

    await openMaintenance(page);
    const beforeDuplicates = relationSnapshot(runtime.sqlitePath);
    const duplicates = await importDownload(page, session.secret, session.stepUps, document(
      [duplicateNode(dupNode, "10.251.6.11", `e2edup${token}a`), duplicateNode(dupNode, "10.251.6.12", `e2edup${token}b`)],
      [duplicateKey(dupKey), duplicateKey(dupKey)],
    ), dupNode);
    expect(duplicates.created).toBe(0);
    expect(duplicates.rejected).toBe(4);
    expect(duplicates.skipped).toBe(0);
    expect(duplicates.disabledImported).toBe(0);
    expect(duplicates.warningsTruncated).toBe(0);
    expectWarningIds(duplicates.warnings, [
      `nodes:0:duplicate_name:${dupNode}`,
      `nodes:1:duplicate_name:${dupNode}`,
      `ssh_keys:0:duplicate_name:${dupKey}`,
      `ssh_keys:1:duplicate_name:${dupKey}`,
    ]);
    expect(relationSnapshot(runtime.sqlitePath)).toBe(beforeDuplicates);
    const duplicateLines = (copy: PanelCopy) => [
      warningLine(copy.node, copy.index(0), dupNode, copy.duplicate),
      warningLine(copy.node, copy.index(1), dupNode, copy.duplicate),
      warningLine(copy.key, copy.index(0), dupKey, copy.duplicate),
      warningLine(copy.key, copy.index(1), dupKey, copy.duplicate),
    ];
    await expectNamePanel(page, {
      copy: PANEL_EN,
      lines: duplicateLines(PANEL_EN),
      exactList: true,
      present: [PANEL_EN.rejectedTitle, PANEL_EN.correctFile],
      absent: nameRouteAbsent(PANEL_EN),
      links: [],
      absentLinks: [PANEL_EN.openKeys, PANEL_EN.openNodes],
    });
    await expectTextFits(page, PANEL_EN.correctFile);
    await showLanguage(page, "zh");
    await expectNamePanel(page, {
      copy: PANEL_ZH,
      lines: duplicateLines(PANEL_ZH),
      exactList: true,
      present: [PANEL_ZH.rejectedTitle, PANEL_ZH.correctFile],
      absent: nameRouteAbsent(PANEL_ZH),
      links: [],
      absentLinks: [PANEL_ZH.openKeys, PANEL_ZH.openNodes],
    });
    await showLanguage(page, "en");

    const source = await createBoundPair(page, names, privateKey, "10.251.6.10", `e2eroute${token}`);
    const beforeKey = relationSnapshot(runtime.sqlitePath);
    const invalidKey = await importDownload(page, session.secret, session.stepUps, document([], [{
      name: names.key,
      username: "e2e-map",
      key_type: "auto",
      allowed_node_names: "not-an-array",
    }]), names.key);
    expect(invalidKey.created).toBe(0);
    expect(invalidKey.rejected).toBe(0);
    expect(invalidKey.skipped).toBe(1);
    expect(invalidKey.disabledImported).toBe(0);
    expect(invalidKey.warningsTruncated).toBe(0);
    expectWarningIds(invalidKey.warnings, [`ssh_keys:0:invalid_reference:${names.key}`]);
    expect(relationSnapshot(runtime.sqlitePath)).toBe(beforeKey);
    const invalidLines = (copy: PanelCopy) => [warningLine(copy.key, copy.index(0), names.key, copy.invalid)];
    await expectNamePanel(page, {
      copy: PANEL_EN,
      lines: invalidLines(PANEL_EN),
      exactList: true,
      present: [PANEL_EN.warningsTitle, PANEL_EN.title, PANEL_EN.reviewScope, PANEL_EN.enableManually],
      absent: [PANEL_EN.rejectedTitle, PANEL_EN.correctFile, PANEL_EN.editKey, PANEL_EN.rebindNode, PANEL_EN.verifyNodes],
      links: [{ name: PANEL_EN.openKeys, href: SSH_KEYS_HREF }],
      absentLinks: [PANEL_EN.openNodes],
    });
    await showTheme(page, "dark");
    await expect(maintenanceOutcome(page)).toContainText(PANEL_EN.invalid);
    await showTheme(page, "light");
    await expectPanelFits(page, PANEL_EN.openKeys);
    await showLanguage(page, "zh");
    await expectNamePanel(page, {
      copy: PANEL_ZH,
      lines: invalidLines(PANEL_ZH),
      exactList: true,
      present: [PANEL_ZH.warningsTitle, PANEL_ZH.title, PANEL_ZH.reviewScope, PANEL_ZH.enableManually],
      absent: [PANEL_ZH.rejectedTitle, PANEL_ZH.correctFile, PANEL_ZH.editKey, PANEL_ZH.rebindNode, PANEL_ZH.verifyNodes],
      links: [{ name: PANEL_ZH.openKeys, href: SSH_KEYS_HREF }],
      absentLinks: [PANEL_ZH.openNodes],
    });
    await showLanguage(page, "en");

    const beforeNode = relationSnapshot(runtime.sqlitePath);
    const conflictNode = await importDownload(page, session.secret, session.stepUps, document([{
      name: names.node,
      host: "10.251.6.10",
      port: 22,
      username: "e2e-map",
      auth_type: "key",
      backup_dir: `e2eroute${token}`,
      ssh_key_name: "",
      ssh_key_id: 8,
    }], []), names.node);
    expect(conflictNode.created).toBe(0);
    expect(conflictNode.rejected).toBe(0);
    expect(conflictNode.skipped).toBe(1);
    expect(conflictNode.disabledImported).toBe(0);
    expect(conflictNode.warningsTruncated).toBe(0);
    expectWarningIds(conflictNode.warnings, [`nodes:0:reference_conflict:${names.node}`]);
    expect(relationSnapshot(runtime.sqlitePath)).toBe(beforeNode);
    const stored = storedPair(runtime.sqlitePath, names);
    expect(stored.nodeId).toBe(source.nodeId);
    expect(stored.sshKeyId).toBe(source.keyId);
    const conflictLines = (copy: PanelCopy) => [warningLine(copy.node, copy.index(0), names.node, copy.conflict)];
    await expectNamePanel(page, {
      copy: PANEL_EN,
      lines: conflictLines(PANEL_EN),
      exactList: true,
      present: [PANEL_EN.warningsTitle, PANEL_EN.title, PANEL_EN.rebindNode],
      absent: [PANEL_EN.rejectedTitle, PANEL_EN.correctFile, PANEL_EN.reviewScope, PANEL_EN.enableManually, PANEL_EN.editKey, PANEL_EN.verifyNodes],
      links: [{ name: PANEL_EN.openNodes, href: NODES_HREF }],
      absentLinks: [PANEL_EN.openKeys],
    });
    await expectPanelFits(page, PANEL_EN.openNodes);
    await showLanguage(page, "zh");
    await expectNamePanel(page, {
      copy: PANEL_ZH,
      lines: conflictLines(PANEL_ZH),
      exactList: true,
      present: [PANEL_ZH.warningsTitle, PANEL_ZH.title, PANEL_ZH.rebindNode],
      absent: [PANEL_ZH.rejectedTitle, PANEL_ZH.correctFile, PANEL_ZH.reviewScope, PANEL_ZH.enableManually, PANEL_ZH.editKey, PANEL_ZH.verifyNodes],
      links: [{ name: PANEL_ZH.openNodes, href: NODES_HREF }],
      absentLinks: [PANEL_ZH.openKeys],
    });
    await showLanguage(page, "en");

    const invalidNewNodeName = `e2e-map,route-invalid-${token}`;
    const invalidNewNode = await importDownload(page, session.secret, session.stepUps, document([{
      ...duplicateNode(invalidNewNodeName, "10.251.6.24", `e2einvalid${token}`),
      auth_type: "password",
      password: "Route-Only-1!",
      ssh_key_name: 42,
    }], []), invalidNewNodeName);
    expect(invalidNewNode.created).toBe(1);
    expect(invalidNewNode.rejected).toBe(0);
    expectWarningIds(invalidNewNode.warnings, [
      `nodes:0:invalid_reference:${invalidNewNodeName}`,
      `nodes:0:unresolved_ssh_key:${invalidNewNodeName}`,
    ]);
    expect(storedNode(runtime.sqlitePath, invalidNewNodeName).sshKeyId).toBeNull();
    for (const [language, copy] of [["en", PANEL_EN], ["zh", PANEL_ZH]] as const) {
      await showLanguage(page, language);
      await expectNamePanel(page, {
        copy,
        lines: [
          warningLine(copy.node, copy.index(0), invalidNewNodeName, copy.invalid),
          warningLine(copy.node, copy.index(0), invalidNewNodeName, copy.unresolvedNode),
        ],
        exactList: true,
        present: [copy.warningsTitle, copy.title, copy.rebindNode],
        absent: [copy.reviewScope, copy.enableManually, copy.editKey, copy.verifyNodes],
        links: [{ name: copy.openNodes, href: NODES_HREF }],
        absentLinks: [copy.openKeys],
      });
      await expectPanelFits(page, copy.openNodes);
    }
    await showLanguage(page, "en");

    const invalidNewKeyName = `e2e-map,route-invalid-key-${token}`;
    const invalidNewKey = await importDownload(page, session.secret, session.stepUps, document([], [{
      name: invalidNewKeyName,
      private_key: privateKey,
      allowed_node_names: "not-an-array",
      allowed_node_ids: "123",
    }]), invalidNewKeyName);
    expect(invalidNewKey.created).toBe(1);
    expect(invalidNewKey.rejected).toBe(0);
    expect(invalidNewKey.disabledImported).toBe(1);
    expectWarningIds(invalidNewKey.warnings, [
      `ssh_keys:0:invalid_reference:${invalidNewKeyName}`,
      `ssh_keys:0:unresolved_node_scope:${invalidNewKeyName}`,
    ]);
    expect(storedKey(runtime.sqlitePath, invalidNewKeyName).disabled).toBe(true);
    for (const [language, copy] of [["en", PANEL_EN], ["zh", PANEL_ZH]] as const) {
      await showLanguage(page, language);
      await expectNamePanel(page, {
        copy,
        lines: [
          warningLine(copy.key, copy.index(0), invalidNewKeyName, copy.invalid),
          warningLine(copy.key, copy.index(0), invalidNewKeyName, copy.unresolvedScope),
        ],
        present: [copy.warningsTitle, copy.title, copy.reviewScope, copy.enableManually],
        absent: [copy.rebindNode, copy.editKey, copy.verifyNodes],
        links: [{ name: copy.openKeys, href: SSH_KEYS_HREF }],
        absentLinks: [copy.openNodes],
      });
      await expectPanelFits(page, copy.openKeys);
    }
    await showLanguage(page, "en");

    const mixDup = `e2e-map,route-mix-${token}`;
    const mixConflict = `e2e-map,route-conflict-${token}`;
    const beforeMix = relationSnapshot(runtime.sqlitePath);
    const mixed = await importDownload(page, session.secret, session.stepUps, document([
      duplicateNode(mixDup, "10.251.6.21", `e2emix${token}a`),
      duplicateNode(mixDup, "10.251.6.22", `e2emix${token}b`),
      {
        name: mixConflict,
        host: "10.251.6.23",
        port: 22,
        username: "e2e-map",
        auth_type: "password",
        password: "Route-Only-1!",
        backup_dir: `e2emix${token}c`,
        ssh_key_name: "",
        ssh_key_id: 8,
      },
    ], [{
      name: names.key,
      username: "e2e-map",
      key_type: "auto",
      allowed_node_names: "not-an-array",
    }]), mixConflict);
    expect(mixed.created).toBe(1);
    expect(mixed.rejected).toBe(2);
    expect(mixed.skipped).toBe(1);
    expect(mixed.disabledImported).toBe(0);
    expect(mixed.warningsTruncated).toBe(0);
    expectWarningIds(mixed.warnings, [
      `nodes:0:duplicate_name:${mixDup}`,
      `nodes:1:duplicate_name:${mixDup}`,
      `nodes:2:reference_conflict:${mixConflict}`,
      `ssh_keys:0:invalid_reference:${names.key}`,
    ]);
    expect(countNamed(runtime.sqlitePath, "nodes", mixDup)).toBe(0);
    const createdConflict = storedNode(runtime.sqlitePath, mixConflict);
    expect(createdConflict.present).toBe(true);
    expect(createdConflict.sshKeyId).toBeNull();
    expect(storedPair(runtime.sqlitePath, names).sshKeyId).toBe(source.keyId);
    assertPriorRowsUnchanged(beforeMix, relationSnapshot(runtime.sqlitePath));
    const mixLines = (copy: PanelCopy) => [
      warningLine(copy.node, copy.index(0), mixDup, copy.duplicate),
      warningLine(copy.node, copy.index(1), mixDup, copy.duplicate),
      warningLine(copy.node, copy.index(2), mixConflict, copy.conflict),
      warningLine(copy.key, copy.index(0), names.key, copy.invalid),
    ];
    await expectNamePanel(page, {
      copy: PANEL_EN,
      lines: mixLines(PANEL_EN),
      exactList: true,
      present: [PANEL_EN.rejectedTitle, PANEL_EN.correctFile, PANEL_EN.title, PANEL_EN.reviewScope, PANEL_EN.enableManually, PANEL_EN.rebindNode],
      absent: [PANEL_EN.warningsTitle, PANEL_EN.editKey, PANEL_EN.verifyNodes],
      links: bothLinks(PANEL_EN),
      absentLinks: [],
    });
    await showLanguage(page, "zh");
    await expectNamePanel(page, {
      copy: PANEL_ZH,
      lines: mixLines(PANEL_ZH),
      exactList: true,
      present: [PANEL_ZH.rejectedTitle, PANEL_ZH.correctFile, PANEL_ZH.title, PANEL_ZH.reviewScope, PANEL_ZH.enableManually, PANEL_ZH.rebindNode],
      absent: [PANEL_ZH.warningsTitle, PANEL_ZH.editKey, PANEL_ZH.verifyNodes],
      links: bothLinks(PANEL_ZH),
      absentLinks: [],
    });
  });

  test("refuses operator, viewer, and unproven admin imports without writes", async ({ request }, testInfo) => {
    test.setTimeout(120_000);
    const runtime = runtimeFiles();
    const token = scenarioToken(`${testInfo.testId}:deny`);
    const password = e2eAdminPassword();
    const operator = `e2e-map-operator-${token}`;
    const viewer = `e2e-map-viewer-${token}`;
    const admin = `e2e-map-admin-${token}`;
    insertIndependentUser(runtime.sqlitePath, operator, "operator");
    insertIndependentUser(runtime.sqlitePath, viewer, "viewer");
    insertIndependentUser(runtime.sqlitePath, admin, "admin");
    const body = {
      version: "1.0",
      data: {
        nodes: [{
          name: `e2e-map,denied-${token}`,
          host: "10.251.5.10",
          port: 22,
          username: "e2e-map",
          auth_type: "password",
          password: "Denied-Only-1!",
          backup_dir: `e2edeny${token}`,
        }],
        ssh_keys: [],
      },
    };
    for (const username of [operator, viewer]) {
      const before = relationSnapshot(runtime.sqlitePath);
      const access = await loginToken(request, username, password, clientAddress(`${testInfo.testId}:${username}`));
      const response = await postImport(request, access, clientAddress(`${testInfo.testId}:${username}:import`), body);
      expect(response.status()).toBe(403);
      expect(await responseMessage(response)).toBe("权限不足");
      expect(relationSnapshot(runtime.sqlitePath)).toBe(before);
    }
    const address = clientAddress(`${testInfo.testId}:${admin}`);
    const enrolled = await activateTotpToken(request, admin, password, address);
    const beforeProof = relationSnapshot(runtime.sqlitePath);
    const missingProof = await postImport(request, enrolled.token, clientAddress(`${testInfo.testId}:${admin}:import`), body);
    expect(missingProof.status()).toBe(403);
    expect(await responseMessage(missingProof)).toBe("需要二次验证");
    expect(relationSnapshot(runtime.sqlitePath)).toBe(beforeProof);
    const proof = await stepUpProof(request, enrolled.token, address, await freshAuthenticatorCode(enrolled.secret));
    const beforeGrant = relationSnapshot(runtime.sqlitePath);
    const missingGrant = await postImport(request, enrolled.token, clientAddress(`${testInfo.testId}:${admin}:grant`), body, proof);
    expect(missingGrant.status()).toBe(403);
    expect(await responseMessage(missingGrant)).toBe("需要临时授权");
    expect(relationSnapshot(runtime.sqlitePath)).toBe(beforeGrant);
  });
});

async function beginAdmin(page: Page, seed: string): Promise<{ secret: string; stepUps: { count: number } }> {
  await isolateClient(page, seed);
  const stepUps = watchStepUps(page);
  const username = `e2e-map-admin-${scenarioToken(seed)}`;
  insertIndependentUser(runtimeFiles().sqlitePath, username, "admin");
  await loginAs(page, username, e2eAdminPassword());
  const secret = await activateTotp(page);
  return { secret, stepUps };
}

async function isolateClient(page: Page, seed: string): Promise<void> {
  await page.setExtraHTTPHeaders({ "X-Forwarded-For": clientAddress(seed) });
}

function clientAddress(seed: string): string {
  const client = createHash("sha256").update(seed).digest("hex");
  return `2001:db8:${client.slice(0, 4)}:${client.slice(4, 8)}::1`;
}

function watchStepUps(page: Page): { count: number } {
  const stepUps = { count: 0 };
  page.on("response", (response) => {
    if (new URL(response.url()).pathname === "/api/v1/auth/step-up") stepUps.count += 1;
  });
  return stepUps;
}

function scenarioToken(seed: string): string {
  return createHash("sha256").update(`${seed}:${randomBytes(4).toString("hex")}`).digest("hex").slice(0, 8);
}

function scenarioNames(token: string): { node: string; key: string } {
  return { node: `e2e-map,node-${token}`, key: `e2e-map,key-${token}` };
}

function distractorNames(token: string): { node: string; key: string } {
  return { node: `e2e-map,other-node-${token}`, key: `e2e-map,other-key-${token}` };
}

async function activateTotp(page: Page): Promise<string> {
  await page.goto("/app/tasks");
  await page.getByRole("link", { name: "Enable two-factor authentication", exact: true }).click();
  await expect(page).toHaveURL(/\/app\/settings\?tab=account$/);
  await page.getByRole("button", { name: "Enable 2FA", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Setup Two-Factor Authentication" });
  const secretElement = dialog.locator("p.font-mono");
  await expect(secretElement).toHaveText(/^[A-Z2-7]+$/);
  const secret = (await secretElement.innerText()).trim();
  await dialog.getByRole("button", { name: "Next", exact: true }).click();
  await dialog.getByLabel("Verification code").fill(await freshAuthenticatorCode(secret));
  const verifyResponsePromise = page.waitForResponse((response) =>
    new URL(response.url()).pathname === "/api/v1/auth/2fa/verify" && response.request().method() === "POST");
  await dialog.getByRole("button", { name: "Verify and Enable", exact: true }).click();
  const verifyResponse = await verifyResponsePromise;
  expect(verifyResponse.ok()).toBe(true);
  await dialog.getByRole("checkbox", { name: "I have saved my recovery codes", exact: true }).check();
  await dialog.getByRole("button", { name: "Finish", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "Disable 2FA", exact: true })).toBeVisible();
  return secret;
}

async function openMaintenance(page: Page): Promise<void> {
  await page.goto("/app/settings?tab=maintenance");
  await expect(page.getByRole("heading", { name: "Maintenance", exact: true })).toBeVisible();
  await expect(page.getByText("Enable two-factor authentication first.")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Import Config", exact: true })).toBeEnabled();
}

async function downloadConfig(page: Page, secret: string, stepUps: { count: number }, sensitive: boolean): Promise<string> {
  const downloadPromise = page.waitForEvent("download", { timeout: 60_000 });
  if (!sensitive) {
    await page.getByRole("button", { name: "Export Config", exact: true }).click();
  } else {
    await page.getByRole("button", { name: "Export Config with Sensitive Fields", exact: true }).click();
    const grant = page.getByRole("dialog", { name: "Sensitive config export temporary grant required" });
    await expect(grant).toBeVisible();
    await grant.locator("#config-import-grant-reason").fill("maintenance export to verify name mapping");
    await grant.getByRole("button", { name: "Grant and export", exact: true }).click();
    await submitFreshProof(page, secret, stepUps);
  }
  const download = await downloadPromise;
  const filePath = await download.path();
  if (!filePath) throw new Error("config download was not captured");
  return readFileSync(filePath, "utf8");
}

async function importDownload(page: Page, secret: string, stepUps: { count: number }, contents: string, marker: string): Promise<ImportBody> {
  const responsePromise = page.waitForResponse((response) =>
    new URL(response.url()).pathname === "/api/v1/config/import" && response.request().method() === "POST", { timeout: 60_000 });
  await page.locator("#settings-panel-maintenance input[type=file]").setInputFiles({
    name: "name-mapping.json",
    mimeType: "application/json",
    buffer: Buffer.from(contents),
  });
  const confirm = page.getByRole("alertdialog");
  await expect(confirm).toBeVisible();
  await confirm.getByRole("button", { name: "Confirm", exact: true }).click();
  const grant = page.getByRole("dialog", { name: "Config import temporary grant required" });
  await expect(grant).toBeVisible();
  await grant.locator("#config-import-grant-reason").fill("restore configuration names after local id shift");
  await grant.getByRole("button", { name: "Grant and import", exact: true }).click();
  await submitFreshProof(page, secret, stepUps);
  const response = await responsePromise;
  expect(response.ok()).toBe(true);
  const posted = response.request().postData() ?? "";
  expect(posted.includes(marker)).toBe(true);
  expect(posted.includes("\"ssh_key_name\"") || posted.includes("\"allowed_node_names\"")).toBe(true);
  return importBody(await response.json());
}

function warningLine(entity: string, indexLabel: string, name: string, sentence: string): string {
  return `${entity} ${indexLabel} (${name}). ${sentence}`;
}

function visibleLine(value: string): string {
  return value.replaceAll("\u00a0", " ").trim().replace(/\s+/g, " ");
}

function bothLinks(copy: PanelCopy): PanelLink[] {
  return [
    { name: copy.openKeys, href: SSH_KEYS_HREF },
    { name: copy.openNodes, href: NODES_HREF },
  ];
}

function disabledGuide(copy: PanelCopy): string[] {
  return [copy.title, copy.editKey, copy.reviewScope, copy.enableManually, copy.verifyNodes];
}

function nameRouteAbsent(copy: PanelCopy): string[] {
  return [copy.warningsTitle, copy.title, copy.reviewScope, copy.enableManually, copy.rebindNode, copy.editKey, copy.verifyNodes];
}

async function expectDisabledGuide(page: Page, copy: PanelCopy): Promise<void> {
  await expectNamePanel(page, {
    copy,
    lines: [],
    present: [copy.missingKey, ...disabledGuide(copy)],
    absent: [],
    links: bothLinks(copy),
    absentLinks: [],
  });
}

async function expectNamePanel(page: Page, spec: {
  copy: PanelCopy;
  lines: string[];
  exactList?: boolean;
  present: string[];
  absent: string[];
  links: PanelLink[];
  absentLinks: string[];
}): Promise<void> {
  const outcome = maintenanceOutcome(page);
  const list = outcome.getByRole("list", { name: spec.copy.listName, exact: true });
  const items = list.getByRole("listitem");
  if (spec.exactList) {
    await expect(items).toHaveCount(spec.lines.length);
    const actual = (await items.allTextContents()).map(visibleLine);
    expect(actual.sort()).toEqual(spec.lines.map(visibleLine).sort());
  } else {
    for (const line of spec.lines) await expect(list).toContainText(line);
  }
  for (const text of spec.present) await expect(outcome).toContainText(text);
  for (const text of spec.absent) await expect(outcome).not.toContainText(text);
  for (const link of spec.links) {
    const item = outcome.getByRole("link", { name: link.name, exact: true });
    await expect(item).toBeVisible();
    await expect(item).toHaveAttribute("href", link.href);
  }
  for (const name of spec.absentLinks) {
    await expect(outcome.getByRole("link", { name, exact: true })).toHaveCount(0);
  }
  await expect(outcome).not.toContainText(/all nodes are available/i);
}

async function showLanguage(page: Page, language: "en" | "zh"): Promise<void> {
  const label = language === "zh" ? "切换到中文" : "Switch to English";
  const button = page.getByRole("button", { name: label, exact: true });
  if (await button.count() === 0) return;
  await button.click();
  const next = language === "zh" ? "Switch to English" : "切换到中文";
  await expect(page.getByRole("button", { name: next, exact: true })).toBeVisible();
}

async function showTheme(page: Page, mode: "dark" | "light"): Promise<void> {
  const next = mode === "dark" ? "Switch to dark mode" : "Switch to light mode";
  const opposite = mode === "dark" ? "Switch to light mode" : "Switch to dark mode";
  const target = page.getByRole("button", { name: next, exact: true });
  if (await target.isVisible()) {
    await target.click();
  } else if (!(await page.getByRole("button", { name: opposite, exact: true }).isVisible())) {
    throw new Error(`theme toggle for ${mode} is not available`);
  }
  await expect.poll(() => page.locator("html").evaluate((element) => element.classList.contains("dark"))).toBe(mode === "dark");
}

async function expectPanelFits(page: Page, linkName: string): Promise<void> {
  await page.setViewportSize({ width: 390, height: 844 });
  const outcome = maintenanceOutcome(page);
  const link = outcome.getByRole("link", { name: linkName, exact: true });
  await link.evaluate((element) => element.scrollIntoView({ block: "center", inline: "nearest" }));
  await expect(link).toBeInViewport();
  await link.focus();
  await expect(link).toBeFocused();
  const box = await link.boundingBox();
  if (!box) throw new Error("remediation link has no box");
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(391);
  const main = await page.locator("#main-content").evaluate((element) => ({
    scrollWidth: element.scrollWidth,
    clientWidth: element.clientWidth,
  }));
  expect(main.scrollWidth).toBeLessThanOrEqual(main.clientWidth + 1);
  const alertBox = await outcome.boundingBox();
  if (!alertBox) throw new Error("import result has no box");
  expect(alertBox.x).toBeGreaterThanOrEqual(0);
  expect(alertBox.x + alertBox.width).toBeLessThanOrEqual(391);
  await page.setViewportSize({ width: 1280, height: 720 });
}

async function expectTextFits(page: Page, text: string): Promise<void> {
  await page.setViewportSize({ width: 390, height: 844 });
  const outcome = maintenanceOutcome(page);
  const target = outcome.getByText(text, { exact: true });
  await target.evaluate((element) => element.scrollIntoView({ block: "center", inline: "nearest" }));
  await expect(target).toBeInViewport();
  const box = await target.boundingBox();
  if (!box) throw new Error("import result text has no box");
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(391);
  const alertBox = await outcome.boundingBox();
  if (!alertBox) throw new Error("import result has no box");
  expect(alertBox.x).toBeGreaterThanOrEqual(0);
  expect(alertBox.x + alertBox.width).toBeLessThanOrEqual(391);
  const main = await page.locator("#main-content").evaluate((element) => ({
    scrollWidth: element.scrollWidth,
    clientWidth: element.clientWidth,
  }));
  expect(main.scrollWidth).toBeLessThanOrEqual(main.clientWidth + 1);
  await page.setViewportSize({ width: 1280, height: 720 });
}

function maintenanceOutcome(page: Page) {
  return page.locator("#settings-panel-maintenance").getByRole("alert");
}

async function createBoundPair(
  page: Page,
  names: { node: string; key: string },
  privateKey: string,
  host: string,
  backupDir: string,
): Promise<{ nodeId: number; keyId: number }> {
  const createdKey = await browserApi(page, "POST", "/api/v1/ssh-keys", {
    name: names.key,
    username: "e2e-map",
    key_type: "auto",
    private_key: privateKey,
    disabled: false,
    allowed_node_ids: "",
  });
  const keyId = positiveId(record(createdKey, "ssh key").id);
  const createdNode = await browserApi(page, "POST", "/api/v1/nodes", {
    name: names.node,
    host,
    port: 22,
    username: "e2e-map",
    auth_type: "key",
    ssh_key_id: keyId,
    backup_dir: backupDir,
  });
  const nodeId = positiveId(record(createdNode, "node").id);
  const updated = await browserApi(page, "PUT", `/api/v1/ssh-keys/${keyId}`, {
    name: names.key,
    username: "e2e-map",
    key_type: "auto",
    allowed_node_ids: String(nodeId),
  });
  expect(record(updated, "ssh key").allowed_node_ids).toBe(String(nodeId));
  return { nodeId, keyId };
}

async function deletePair(page: Page, nodeId: number, keyId: number): Promise<void> {
  await browserApi(page, "DELETE", `/api/v1/nodes/${nodeId}`);
  await browserApi(page, "DELETE", `/api/v1/ssh-keys/${keyId}`);
}

async function listedPair(page: Page, names: { node: string; key: string }): Promise<{ node: ListedNode; key: ListedKey }> {
  const nodes = await browserApi(page, "GET", "/api/v1/nodes?include_archived=true");
  const keys = await browserApi(page, "GET", "/api/v1/ssh-keys");
  return {
    node: listedNode(nodes, names.node),
    key: listedKey(keys, names.key),
  };
}

async function browserApi(page: Page, method: string, apiPath: string, body?: unknown): Promise<unknown> {
  const result = await page.evaluate(async ({ method, apiPath, body }) => {
    const token = sessionStorage.getItem("xirang-auth-token");
    if (!token) return { status: 0, payload: null };
    const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
    const init: RequestInit = { method, headers };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    const response = await fetch(apiPath, init);
    const text = await response.text();
    let payload: unknown = null;
    if (text) {
      try {
        payload = JSON.parse(text) as unknown;
      } catch {
        payload = null;
      }
    }
    return { status: response.status, payload };
  }, { method, apiPath, body });
  const ok = method === "POST" ? result.status === 201 || result.status === 200 : result.status === 200;
  if (!ok) {
    const message = messageOf(result.payload);
    throw new Error(`${method} ${apiPath} returned ${result.status}${message ? `: ${message}` : ""}`);
  }
  if (!result.payload || typeof result.payload !== "object" || !("data" in result.payload)) {
    throw new Error(`${method} ${apiPath} returned no data`);
  }
  return result.payload.data;
}

function record(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} response is not an object`);
  return value as Record<string, unknown>;
}

function positiveId(value: unknown): number {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 1) throw new Error("response id is invalid");
  return value;
}

function listedNode(value: unknown, name: string): ListedNode {
  const row = rows(value).find((item) => item.name === name);
  if (!row) throw new Error(`node ${name} is not listed`);
  const sshKeyId = row.ssh_key_id;
  return {
    id: positiveId(row.id),
    name,
    sshKeyId: sshKeyId === null ? null : positiveId(sshKeyId),
  };
}

function listedKey(value: unknown, name: string): ListedKey {
  const row = rows(value).find((item) => item.name === name);
  if (!row) throw new Error(`ssh key ${name} is not listed`);
  if (typeof row.allowed_node_ids !== "string" || typeof row.disabled !== "boolean") {
    throw new Error(`ssh key ${name} is missing scope fields`);
  }
  return { id: positiveId(row.id), name, disabled: row.disabled, allowedNodeIds: row.allowed_node_ids };
}

function rows(value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value)) throw new Error("list response is not an array");
  return value.filter((item): item is Record<string, unknown> => Boolean(item) && typeof item === "object" && !Array.isArray(item));
}

function parseExport(raw: string): { data: Record<string, unknown> } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw) as unknown;
  } catch {
    throw new Error(`config download is not json (${raw.length} bytes)`);
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("config download is not an object");
  const version = "version" in parsed ? parsed.version : undefined;
  const data = "data" in parsed ? parsed.data : undefined;
  if (version !== "2.0" || !data || typeof data !== "object" || Array.isArray(data)) {
    throw new Error("config download is not a v2 document");
  }
  return { data: data as Record<string, unknown> };
}

function assertExportedPair(
  exported: { data: Record<string, unknown> },
  names: { node: string; key: string },
  nodeId: number,
  keyId: number,
  sensitive: boolean,
): void {
  const node = exportedRecord(exported.data.nodes, names.node);
  const key = exportedRecord(exported.data.ssh_keys, names.key);
  expect(node.ssh_key_name).toBe(names.key);
  expect(node.ssh_key_id).toBe(keyId);
  expect(key.allowed_node_names).toEqual([names.node]);
  expect(key.allowed_node_ids).toBe(String(nodeId));
  expect(Object.hasOwn(key, "private_key")).toBe(sensitive);
  if (sensitive) {
    expect(typeof key.private_key === "string" && key.private_key.includes("PRIVATE KEY")).toBe(true);
  }
}

function exportedRecord(value: unknown, name: string): Record<string, unknown> {
  const found = rows(value).filter((item) => item.name === name);
  if (found.length !== 1) throw new Error(`expected one exported record named ${name}`);
  return found[0] ?? {};
}

function importBody(payload: unknown): ImportBody {
  const data = record(payload, "import").data;
  const body = record(data, "import data");
  const warnings = Array.isArray(body.warnings) ? body.warnings : [];
  return {
    created: count(body.created),
    rejected: count(body.rejected),
    skipped: count(body.skipped),
    disabledImported: count(body.disabled_imported),
    warningsTruncated: count(body.warnings_truncated),
    warnings: warnings.map((entry, index) => {
      const warning = record(entry, `warning ${index}`);
      if (typeof warning.entity !== "string" || typeof warning.code !== "string" || typeof warning.index !== "number") {
        throw new Error("import warning shape is invalid");
      }
      const name = typeof warning.name === "string" ? warning.name : undefined;
      if (name?.includes("PRIVATE KEY")) throw new Error("import warning exposed key material");
      return { entity: warning.entity, index: warning.index, code: warning.code, name };
    }),
  };
}

function count(value: unknown): number {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0) throw new Error("import count is invalid");
  return value;
}

function expectWarningIds(warnings: Warning[], expected: string[]): void {
  const identities = warnings.map((warning) => `${warning.entity}:${warning.index}:${warning.code}:${warning.name ?? ""}`);
  expect([...identities].sort()).toEqual([...expected].sort());
}

function requireWarning(warnings: Warning[], entity: string, code: string, name: string): void {
  if (warnings.some((warning) => warning.entity === entity && warning.code === code && warning.name === name)) return;
  throw new Error(`missing ${entity}/${code} for ${name}; saw ${warnings.map((warning) => `${warning.entity}:${warning.code}:${warning.name ?? ""}`).join(", ")}`);
}

function scopeIds(value: string): string[] {
  return value.split(",").map((item) => item.trim()).filter((item) => item !== "");
}

function messageOf(payload: unknown): string {
  if (!payload || typeof payload !== "object" || !("message" in payload)) return "";
  const message = payload.message;
  return typeof message === "string" ? message.slice(0, 180) : "";
}

async function loginToken(request: APIRequestContext, username: string, password: string, address: string): Promise<string> {
  const response = await request.post("/api/v1/auth/login", {
    headers: { "X-Forwarded-For": address },
    data: { username, password },
  });
  expect(response.status()).toBe(200);
  const data = record(await response.json(), "login").data;
  const body = record(data, "login data");
  if (body.requires_2fa === true) throw new Error(`${username} requires 2FA before activation`);
  if (typeof body.token !== "string" || body.token === "") throw new Error(`${username} login did not return a token`);
  return body.token;
}

async function activateTotpToken(
  request: APIRequestContext,
  username: string,
  password: string,
  address: string,
): Promise<{ token: string; secret: string }> {
  const token = await loginToken(request, username, password, address);
  const setup = record(await apiData(request, "/api/v1/auth/2fa/setup", token, address), "totp setup");
  const secret = setup.secret;
  const enrollmentId = setup.enrollment_id;
  if (typeof secret !== "string" || secret === "" || typeof enrollmentId !== "string" || enrollmentId === "") {
    throw new Error("totp setup did not return an enrollment");
  }
  const verified = record(await apiData(request, "/api/v1/auth/2fa/verify", token, address, {
    code: await freshAuthenticatorCode(secret),
    enrollment_id: enrollmentId,
  }), "totp verify");
  if (typeof verified.token !== "string" || verified.token === "") throw new Error("totp verify did not replace the session");
  return { token: verified.token, secret };
}

async function stepUpProof(request: APIRequestContext, token: string, address: string, code: string): Promise<string> {
  const proofBody = record(await apiData(request, "/api/v1/auth/step-up", token, address, {
    code,
    step_up_action: "config.import",
  }), "step-up");
  if (typeof proofBody.proof !== "string" || proofBody.proof === "") throw new Error("step-up did not return a proof");
  return proofBody.proof;
}

async function postImport(
  request: APIRequestContext,
  token: string,
  address: string,
  body: unknown,
  proof?: string,
): Promise<APIResponse> {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${token}`,
    "X-Forwarded-For": address,
  };
  if (proof) headers[STEP_UP_HEADER] = proof;
  return request.post("/api/v1/config/import?conflict=skip", { headers, data: body });
}

async function responseMessage(response: APIResponse): Promise<string> {
  return messageOf(await response.json());
}

async function apiData(request: APIRequestContext, apiPath: string, token: string, address: string, body?: unknown): Promise<unknown> {
  const response = await request.post(apiPath, {
    headers: { Authorization: `Bearer ${token}`, "X-Forwarded-For": address },
    data: body ?? {},
  });
  if (!response.ok()) throw new Error(`POST ${apiPath} returned ${response.status()}: ${messageOf(await response.json())}`);
  return record(await response.json(), apiPath).data;
}

function runtimeFiles(): RuntimeFiles {
  const runtimeDir = (process.env.E2E_RUNTIME_DIR ?? "").trim();
  if (!path.isAbsolute(runtimeDir)) throw new Error("E2E_RUNTIME_DIR must be absolute");
  const runtimeReal = realpathSync(runtimeDir);
  const fixtureInfo = lstatSync(path.join(runtimeReal, "fixture.json"));
  if (fixtureInfo.isSymbolicLink() || !fixtureInfo.isFile()) throw new Error("isolated runtime fixture must be a regular file");
  const parsed: unknown = JSON.parse(readFileSync(path.join(runtimeReal, "fixture.json"), "utf8"));
  if (!parsed || typeof parsed !== "object") throw new Error("isolated runtime fixture is not an object");
  return {
    sqlitePath: fixtureFile("sqlitePath" in parsed ? parsed.sqlitePath : undefined, runtimeReal),
    privateKeyPath: fixtureFile("privateKeyPath" in parsed ? parsed.privateKeyPath : undefined, runtimeReal),
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

function readRuntimePrivateKey(filePath: string): string {
  const key = readFileSync(filePath, "utf8");
  if (!key.includes("PRIVATE KEY") || key.includes("\0") || key.includes("ENCRYPTED")) {
    throw new Error("isolated SSH private key is not an unencrypted private key");
  }
  return key.endsWith("\n") ? key : `${key}\n`;
}

function sqlite(dbPath: string, sql: string): string {
  const uri = pathToFileURL(path.resolve(dbPath));
  uri.searchParams.set("mode", "rw");
  try {
    // -json is machine-readable on current and CI sqlite. Default list mode prints char(31) as "^_".
    return execFileSync("sqlite3", ["-json", "-batch", "-bail", "-init", "/dev/null", "-cmd", ".timeout 5000", uri.href, sql], {
      encoding: "utf8",
      timeout: 10_000,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : "sqlite3 failed";
    throw new Error(message.replace(/PRIVATE KEY[^-]*-----/g, "PRIVATE KEY [redacted]").slice(0, 400));
  }
}

function sqliteRows(dbPath: string, sql: string): Record<string, unknown>[] {
  const output = sqlite(dbPath, sql).trim();
  if (output === "") return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(output);
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

type RelationRecord = {
  entity: "nodes" | "ssh_keys";
  id: number;
  name: string;
  ssh_key_id: number | null;
  allowed_node_ids: string | null;
  disabled: number | null;
};

function relationSnapshot(dbPath: string): string {
  const rows = sqliteRows(dbPath, [
    "SELECT 'nodes' AS entity, id, name, ssh_key_id, NULL AS allowed_node_ids, NULL AS disabled FROM nodes",
    "UNION ALL",
    "SELECT 'ssh_keys', id, name, NULL, allowed_node_ids, disabled FROM ssh_keys",
    "ORDER BY entity, id;",
  ].join("\n"));
  const records: RelationRecord[] = rows.map((row) => {
    const id = requireInt(row.id, "relation id");
    if (typeof row.name !== "string" || id < 1) throw new Error("relation row is invalid");
    const name = row.name;
    if (row.entity === "nodes") {
      if (row.allowed_node_ids !== null || row.disabled !== null) throw new Error("node relation row is invalid");
      return {
        entity: "nodes",
        id,
        name,
        ssh_key_id: row.ssh_key_id === null ? null : requireInt(row.ssh_key_id, "node ssh_key_id"),
        allowed_node_ids: null,
        disabled: null,
      };
    }
    if (row.entity !== "ssh_keys") throw new Error("relation row is invalid");
    const disabled = requireInt(row.disabled, "ssh key disabled");
    if ((disabled !== 0 && disabled !== 1) || typeof row.allowed_node_ids !== "string" || row.ssh_key_id !== null) {
      throw new Error("ssh key relation row is invalid");
    }
    return {
      entity: "ssh_keys",
      id,
      name,
      ssh_key_id: null,
      allowed_node_ids: row.allowed_node_ids,
      disabled,
    };
  });
  return JSON.stringify(records);
}

function assertPriorRowsUnchanged(before: string, after: string): void {
  const next = new Set(parseRelation(after));
  for (const record of parseRelation(before)) {
    if (!next.has(record)) throw new Error("import changed an existing node or ssh key");
  }
}

function parseRelation(snapshot: string): string[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(snapshot);
  } catch {
    throw new Error("relation snapshot is invalid");
  }
  if (!Array.isArray(parsed)) throw new Error("relation snapshot is invalid");
  return parsed.map((record) => {
    if (!record || typeof record !== "object" || Array.isArray(record)) throw new Error("relation snapshot is invalid");
    return JSON.stringify(record);
  });
}

function insertIndependentUser(dbPath: string, username: string, role: "admin" | "operator" | "viewer"): void {
  if (!/^e2e-map-[a-z]+-[0-9a-f]{8}$/.test(username)) throw new Error("refusing unexpected independent username");
  const rows = sqliteRows(dbPath, [
    "INSERT INTO users (",
    "username, password_hash, role, totp_secret, totp_enabled, recovery_codes,",
    "token_version, totp_enrollment_id, totp_enrollment_expires_at, onboarded,",
    "created_at, updated_at",
    ") SELECT",
    `'${username}',`,
    "(SELECT password_hash FROM users WHERE username = 'admin'),",
    `'${role}', '', 0, '', 0, '', NULL, 1, datetime('now'), datetime('now')`,
    "WHERE (SELECT COUNT(*) FROM users WHERE username = 'admin') = 1",
    `AND (SELECT COUNT(*) FROM users WHERE username = '${username}') = 0;`,
    "SELECT changes() AS changes,",
    `(SELECT role FROM users WHERE username = '${username}') AS role,`,
    `(SELECT totp_enabled FROM users WHERE username = '${username}') AS totp_enabled;`,
  ].join("\n"));
  const row = rows[0];
  if (rows.length !== 1 || row === undefined || row.changes !== 1 || row.role !== role || row.totp_enabled !== 0) {
    throw new Error(`independent user ${username} was not inserted`);
  }
}

type StoredNode = { present: boolean; id: number; sshKeyId: number | null };
type StoredKey = { present: boolean; id: number; allowedNodeIds: string; disabled: boolean };

function storedPair(dbPath: string, names: { node: string; key: string }): { nodeId: number; sshKeyId: number; allowedNodeIds: string; disabled: boolean } {
  const node = storedNode(dbPath, names.node);
  const key = storedKey(dbPath, names.key);
  if (!node.present || node.sshKeyId === null || !key.present) throw new Error("stored name-mapping pair is incomplete");
  return { nodeId: node.id, sshKeyId: node.sshKeyId, allowedNodeIds: key.allowedNodeIds, disabled: key.disabled };
}

function storedNode(dbPath: string, name: string): StoredNode {
  const rows = sqliteRows(dbPath, `SELECT id, ssh_key_id FROM nodes WHERE name = '${sqlText(name)}';`);
  const row = rows[0];
  if (rows.length === 0) return { present: false, id: 0, sshKeyId: null };
  if (rows.length !== 1 || row === undefined) throw new Error("stored node id is invalid");
  const id = requireInt(row.id, "stored node id");
  if (id < 1) throw new Error("stored node id is invalid");
  return { present: true, id, sshKeyId: row.ssh_key_id === null ? null : requireInt(row.ssh_key_id, "stored node ssh key") };
}

function storedKey(dbPath: string, name: string): StoredKey {
  const rows = sqliteRows(dbPath, `SELECT id, allowed_node_ids, disabled FROM ssh_keys WHERE name = '${sqlText(name)}';`);
  const row = rows[0];
  if (rows.length === 0) return { present: false, id: 0, allowedNodeIds: "", disabled: false };
  if (rows.length !== 1 || row === undefined) throw new Error("stored ssh key row is invalid");
  const id = requireInt(row.id, "stored ssh key id");
  const disabled = requireInt(row.disabled, "stored ssh key disabled");
  if (id < 1 || (disabled !== 0 && disabled !== 1) || typeof row.allowed_node_ids !== "string") {
    throw new Error("stored ssh key row is invalid");
  }
  return { present: true, id, allowedNodeIds: row.allowed_node_ids, disabled: disabled === 1 };
}

function countNamed(dbPath: string, table: "nodes" | "ssh_keys", name: string): number {
  const rows = sqliteRows(dbPath, `SELECT COUNT(*) AS count FROM ${table} WHERE name = '${sqlText(name)}';`);
  const row = rows[0];
  if (rows.length !== 1 || row === undefined) throw new Error("name count is invalid");
  const countValue = requireInt(row.count, "name count");
  if (countValue < 0) throw new Error("name count is invalid");
  return countValue;
}

function sqlText(value: string): string {
  if (!/^e2e-map[a-z0-9,-]+$/.test(value)) throw new Error("refusing unexpected sql text");
  return value;
}
