import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync, lstatSync, readFileSync, realpathSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Seeds one uniquely named, read-only-preview fixture into an isolated
// real-backend runtime created by playwright.real-backend.config.ts (or a
// manual runtime created with the same protocol). It is shared by
// escalation-preview.spec.ts and by manual Bun sessions; it never runs a test
// runner, never starts services, and refuses any database outside
// <repo>/.tmp/agent/lifecycle-p1-e2e/runtime.*.
//
// Safety invariants of the seeded rows:
// - the open alert triggers one hour in the future, so the running escalation
//   engine only ever sees "waiting" while the fixture is fresh;
// - every alert carries a durable, non-pending delivery decision, so the
//   dispatcher has nothing to replay;
// - deliveries are only terminal (sent/failed), so no retry worker picks them up;
// - channel endpoints are loopback discard placeholders that cannot reach out.

export type EscalationPreviewFixture = {
  openAlertId: number;
  resolvedAlertId: number;
  policyId: number;
  nodeId: number;
};

const REPOSITORY_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRATCH_BOUNDARY = path.join(REPOSITORY_ROOT, ".tmp");
const LIFECYCLE_ROOT = path.join(SCRATCH_BOUNDARY, "agent", "lifecycle-p1-e2e");
const RUNTIME_NAME = /^runtime\.[A-Za-z0-9]+$/;
const SAME_RUN_MARK = /^[0-9a-f]{32}$/;
const LOOPBACK_DISCARD_ENDPOINT = "http://127.0.0.1:9/xirang-e2e-escalation-preview";

const SEED_PROGRAM = String.raw`
import json
import sqlite3
import sys
import urllib.parse

request = json.load(sys.stdin)
fixture = request["fixture"]
prefix = fixture["prefix"]


def table_columns(connection, table):
    rows = connection.execute('PRAGMA table_info("%s")' % table).fetchall()
    if not rows:
        raise RuntimeError("isolated runtime is missing table " + table)
    return rows


def insert(connection, table, values):
    info = table_columns(connection, table)
    names = {row[1] for row in info}
    unknown = sorted(set(values) - names)
    if unknown:
        raise RuntimeError("%s has no columns %s" % (table, ", ".join(unknown)))
    for _cid, name, _type, notnull, default, pk in info:
        if notnull and default is None and not pk and name not in values:
            raise RuntimeError("%s.%s requires a fixture value" % (table, name))
    keys = list(values)
    columns = ",".join('"%s"' % key for key in keys)
    placeholders = ",".join("?" * len(keys))
    cursor = connection.execute(
        'INSERT INTO "%s" (%s) VALUES (%s)' % (table, columns, placeholders),
        [values[key] for key in keys],
    )
    return cursor.lastrowid


def one(connection, sql, parameters=()):
    rows = connection.execute(sql, parameters).fetchall()
    if len(rows) != 1:
        raise RuntimeError("expected exactly one row")
    return rows[0][0]


uri = "file:" + urllib.parse.quote(request["database"]) + "?mode=rw"
connection = sqlite3.connect(uri, uri=True, timeout=5, isolation_level=None)
try:
    connection.execute("PRAGMA busy_timeout = 5000")
    connection.execute("BEGIN IMMEDIATE")
    try:
        like = prefix + "%"
        for table in ("escalation_policies", "integrations", "nodes", "silences"):
            if one(connection, 'SELECT COUNT(*) FROM "%s" WHERE name LIKE ?' % table, (like,)) != 0:
                raise RuntimeError("fixture prefix already present in " + table)
        admin_id = one(connection, "SELECT id FROM users WHERE username = 'admin'")
        now = fixture["now"]

        enabled_id = insert(connection, "integrations", {
            "type": "webhook", "name": fixture["enabledChannel"], "endpoint": fixture["endpoint"],
            "secret": "", "enabled": 1, "fail_threshold": 1, "cooldown_minutes": 5, "proxy_url": "",
            "created_at": now, "updated_at": now,
        })
        disabled_id = insert(connection, "integrations", {
            "type": "webhook", "name": fixture["disabledChannel"], "endpoint": fixture["endpoint"],
            "secret": "", "enabled": 0, "fail_threshold": 1, "cooldown_minutes": 5, "proxy_url": "",
            "created_at": now, "updated_at": now,
        })
        highest = one(connection, "SELECT COALESCE(MAX(id), 0) FROM integrations")
        missing_id = highest + 1000000
        if one(connection, "SELECT COUNT(*) FROM integrations WHERE id = ?", (missing_id,)) != 0:
            raise RuntimeError("missing channel id is unexpectedly present")

        levels = [
            {"delay_seconds": 0, "integration_ids": [enabled_id, disabled_id, missing_id],
             "severity_override": "", "tags": ["e2e-level-1"]},
            {"delay_seconds": 600, "integration_ids": [enabled_id],
             "severity_override": "critical", "tags": ["e2e-level-2"]},
        ]
        policy_id = insert(connection, "escalation_policies", {
            "name": fixture["policyName"], "description": "", "min_severity": "warning", "enabled": 1,
            "levels": json.dumps(levels, separators=(",", ":")), "created_at": now, "updated_at": now,
        })
        node_id = insert(connection, "nodes", {
            "name": fixture["nodeName"], "host": "127.0.0.1", "port": 1, "username": "e2e",
            "auth_type": "key", "password": "", "private_key": "", "tags": "e2e,escalation-preview",
            "status": "offline", "base_path": "", "backup_dir": fixture["nodeName"], "use_sudo": 0,
            "archived": 1, "escalation_policy_id": policy_id, "created_at": now, "updated_at": now,
        })
        silence_ids = []
        for suffix in ("a", "b"):
            silence_ids.append(insert(connection, "silences", {
                "name": "%s-silence-%s" % (prefix, suffix), "match_node_id": node_id,
                "match_category": "", "match_tags": "[]", "starts_at": fixture["silenceStart"],
                "ends_at": fixture["silenceEnd"], "created_by": admin_id, "note": "",
                "created_at": now, "updated_at": now,
            }))

        open_alert_id = insert(connection, "alerts", {
            "node_id": node_id, "node_name": fixture["nodeName"], "policy_name": "",
            "severity": "warning", "status": "open", "error_code": fixture["openErrorCode"],
            "message": fixture["openMessage"], "retryable": 0, "triggered_at": fixture["openTriggeredAt"],
            "tags": "[]", "last_level_fired": -1, "delivery_decision": "escalated",
            "delivery_reason": "escalation", "delivery_decided_at": now,
            "created_at": now, "updated_at": now,
        })
        resolved_alert_id = insert(connection, "alerts", {
            "node_id": node_id, "node_name": fixture["nodeName"], "policy_name": "",
            "severity": "warning", "status": "resolved", "error_code": fixture["resolvedErrorCode"],
            "message": fixture["resolvedMessage"], "retryable": 0,
            "triggered_at": fixture["resolvedTriggeredAt"], "tags": "[]", "last_level_fired": 0,
            "delivery_decision": "direct", "delivery_reason": "", "delivery_decided_at": fixture["resolvedTriggeredAt"],
            "created_at": fixture["resolvedTriggeredAt"], "updated_at": now,
        })
        insert(connection, "alert_escalation_events", {
            "alert_id": resolved_alert_id, "escalation_policy_id": policy_id, "level_index": 0,
            "integration_ids": json.dumps([enabled_id]), "severity_before": "warning",
            "severity_after": "warning", "tags_added": json.dumps(["e2e-level-1"]),
            "fired_at": fixture["resolvedTriggeredAt"],
        })
        for index, (integration_id, status, attempts, error, sent_at) in enumerate((
            (enabled_id, "sent", 1, "", fixture["resolvedTriggeredAt"]),
            (enabled_id, "failed", 4, "fixture terminal failure", None),
        )):
            insert(connection, "alert_deliveries", {
                "alert_id": resolved_alert_id, "integration_id": integration_id, "status": status,
                "decision": "deliver", "delivery_key": "%s-delivery-%d" % (prefix, index),
                "attempt_count": attempts, "attempt_id": "", "last_error": error, "sent_at": sent_at,
                "created_at": fixture["resolvedTriggeredAt"], "updated_at": now,
            })

        pending_alerts = one(connection,
            "SELECT COUNT(*) FROM alerts WHERE id IN (?, ?) AND (delivery_decision IS NULL OR delivery_decision IN ('', 'pending'))",
            (open_alert_id, resolved_alert_id))
        retryable = one(connection,
            "SELECT COUNT(*) FROM alert_deliveries WHERE alert_id IN (?, ?) AND status IN ('pending', 'sending', 'retrying')",
            (open_alert_id, resolved_alert_id))
        if pending_alerts != 0 or retryable != 0:
            raise RuntimeError("fixture would be replayed by background workers")
        connection.execute("COMMIT")
    except BaseException:
        connection.execute("ROLLBACK")
        raise
finally:
    connection.close()

print(json.dumps({
    "openAlertId": open_alert_id,
    "resolvedAlertId": resolved_alert_id,
    "policyId": policy_id,
    "nodeId": node_id,
}))
`;

function assertRealDirectory(directory: string, label: string): void {
  const info = lstatSync(directory);
  if (info.isSymbolicLink() || !info.isDirectory()) throw new Error(`${label} must be a real directory`);
}

function assertRegularFile(file: string, label: string): void {
  const info = lstatSync(file);
  if (info.isSymbolicLink() || !info.isFile()) throw new Error(`${label} must be a regular file`);
}

// Resolves the isolated runtime database or throws. Every path component from
// the repository scratch boundary down to xirang.db must be a real (non-link)
// entry, and the runtime must be the one marked for this run.
function isolatedDatabase(runtimeDir: string): string {
  if (typeof runtimeDir !== "string" || !path.isAbsolute(runtimeDir)) throw new Error("runtimeDir must be an absolute path");
  for (const directory of [SCRATCH_BOUNDARY, path.dirname(LIFECYCLE_ROOT), LIFECYCLE_ROOT]) {
    assertRealDirectory(directory, "isolated scratch boundary");
  }
  const lifecycleReal = realpathSync(LIFECYCLE_ROOT);
  const requested = path.resolve(runtimeDir);
  // Lexically inside the boundary, and the runtime directory itself is a
  // real directory (the boundary components were checked above).
  if (path.dirname(requested) !== LIFECYCLE_ROOT || !RUNTIME_NAME.test(path.basename(requested))) {
    throw new Error("refusing a runtime outside .tmp/agent/lifecycle-p1-e2e/runtime.*");
  }
  assertRealDirectory(requested, "isolated runtime");
  const runtimeReal = realpathSync(requested);
  if (path.dirname(runtimeReal) !== lifecycleReal) throw new Error("isolated runtime escaped the scratch boundary");

  const mark = (process.env.E2E_RUNTIME_SAME_RUN ?? "").trim();
  const markedRuntime = (process.env.E2E_RUNTIME_DIR ?? "").trim();
  if (!SAME_RUN_MARK.test(mark)) throw new Error("E2E_RUNTIME_SAME_RUN must carry this run's 32-hex marker");
  if (!path.isAbsolute(markedRuntime) || realpathSync(markedRuntime) !== runtimeReal) {
    throw new Error("runtimeDir must be the runtime marked by E2E_RUNTIME_DIR for this run");
  }

  const fixturePath = path.join(runtimeReal, "fixture.json");
  assertRegularFile(fixturePath, "isolated runtime fixture.json");
  const parsed: unknown = JSON.parse(readFileSync(fixturePath, "utf8"));
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("isolated runtime fixture.json is not an object");
  const sqlitePath = (parsed as Record<string, unknown>).sqlitePath;
  const expected = path.join(runtimeReal, "xirang.db");
  if (typeof sqlitePath !== "string" || !path.isAbsolute(sqlitePath)) throw new Error("isolated runtime sqlite path is invalid");
  assertRegularFile(sqlitePath, "isolated runtime database");
  assertRegularFile(expected, "isolated runtime database");
  if (realpathSync(sqlitePath) !== expected || realpathSync(expected) !== expected) {
    throw new Error("isolated runtime database must be <runtime>/xirang.db");
  }
  for (const sidecar of [`${expected}-wal`, `${expected}-shm`, `${expected}-journal`]) {
    if (existsSync(sidecar)) assertRegularFile(sidecar, "isolated runtime database sidecar");
  }
  return expected;
}

function sqliteTime(date: Date): string {
  return date.toISOString().replace("T", " ").replace(/\.\d{3}Z$/, "+00:00");
}

export function seedEscalationPreviewFixture(runtimeDir: string): EscalationPreviewFixture {
  const database = isolatedDatabase(runtimeDir);
  const token = randomBytes(4).toString("hex");
  const prefix = `e2e-esc-preview-${token}`;
  const now = new Date(Math.floor(Date.now() / 1000) * 1000);
  const openTriggered = new Date(now.getTime() + 60 * 60 * 1000);
  const resolvedTriggered = new Date(now.getTime() - 2 * 60 * 60 * 1000);
  const code = `XR-E2E-${token.toUpperCase()}`;
  const fixture = {
    prefix,
    now: sqliteTime(now),
    endpoint: LOOPBACK_DISCARD_ENDPOINT,
    policyName: `${prefix}-policy`,
    // Deliberately long and unbroken to exercise wrapping in narrow layouts.
    enabledChannel: `${prefix}-enabled-${"overflow-check-segment-".repeat(4)}end`,
    disabledChannel: `${prefix}-disabled`,
    nodeName: `${prefix}-node`,
    openErrorCode: `${code}-OPEN`,
    resolvedErrorCode: `${code}-RESOLVED`,
    openMessage: `E2E escalation preview fixture (open) ${token}`,
    resolvedMessage: `E2E escalation preview fixture (resolved) ${token}`,
    openTriggeredAt: sqliteTime(openTriggered),
    resolvedTriggeredAt: sqliteTime(resolvedTriggered),
    // Covers only the projected second level (due at trigger+600s).
    silenceStart: sqliteTime(new Date(openTriggered.getTime() + 300 * 1000)),
    silenceEnd: sqliteTime(new Date(openTriggered.getTime() + 1200 * 1000)),
  };
  if (fixture.enabledChannel.length > 128) throw new Error("fixture channel name exceeds the stored limit");

  const result = spawnSync("python3", ["-c", SEED_PROGRAM], {
    input: JSON.stringify({ database, fixture }),
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
    timeout: 30_000,
    env: { PATH: process.env.PATH ?? "/usr/bin:/bin", LANG: "C.UTF-8" },
  });
  if (result.status !== 0) {
    const reason = (result.stderr ?? "").trim().split("\n").pop() ?? "";
    throw new Error(`escalation preview fixture seeding failed: ${reason.slice(0, 300)}`);
  }
  const parsed: unknown = JSON.parse(result.stdout);
  if (!parsed || typeof parsed !== "object") throw new Error("seeder returned no fixture identity");
  const identity = parsed as Record<string, unknown>;
  const ids = ["openAlertId", "resolvedAlertId", "policyId", "nodeId"] as const;
  for (const key of ids) {
    const value = identity[key];
    if (typeof value !== "number" || !Number.isSafeInteger(value) || value <= 0) throw new Error(`seeder returned an invalid ${key}`);
  }
  return {
    openAlertId: identity.openAlertId as number,
    resolvedAlertId: identity.resolvedAlertId as number,
    policyId: identity.policyId as number,
    nodeId: identity.nodeId as number,
  };
}
