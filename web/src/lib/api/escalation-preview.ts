import { request } from "./core";
import type { EscalationPolicyInput } from "./escalation";

// Contract constants shared with backend/internal/escalation/preview.go.
export const PREVIEW_TTL_MS = 60_000;
export const PREVIEW_TICK_SECONDS = 30;
/** floor(MaxInt64 / 1e9): the largest delay representable as a Go time.Duration. */
export const MAX_PREVIEW_DELAY_SECONDS = 9_223_372_036;
export const MAX_PREVIEW_LEVELS = 5;
export const MAX_PREVIEW_CHANNELS_PER_LEVEL = 100;
export const MAX_PREVIEW_DRAFT_TAGS = 10;
export const MAX_PREVIEW_TAG_BYTES = 32;
export const MAX_PREVIEW_ERROR_CODE_BYTES = 128;
const MAX_PREVIEW_NAME_BYTES = 100;
const MAX_PREVIEW_DESCRIPTION_BYTES = 2000;
const MAX_MATCHED_SILENCES = 1000;

const SEVERITIES = ["info", "warning", "critical"] as const;
const ALERT_STATUSES = ["open", "acked", "resolved"] as const;
const PREVIEW_STATUSES = [
  "projected",
  "not_open",
  "no_policy",
  "disabled",
  "below_minimum",
  "complete",
  "unsupported_state",
] as const;
const POLICY_SOURCES = ["draft", "task", "policy", "slo", "node", "none"] as const;
const DELIVERY_DECISIONS = ["pending", "direct", "suppressed", "escalated", "no_channel", "unknown"] as const;
const WARNINGS = [
  "node_missing",
  "policy_changed_since_history",
  "unsupported_configuration",
  "unsupported_alert_state",
  "event_cursor_conflict",
  "unsupported_time",
] as const;
/** Only these warnings may accompany a result that is not unsupported_state. */
const PROJECTABLE_WARNINGS: readonly EscalationPreviewWarning[] = ["node_missing", "policy_changed_since_history"];
const STEP_STATUSES = ["would_enqueue", "silenced", "no_enabled_channels", "blocked_minimum", "blocked_prior"] as const;
const CHANNEL_STATES = ["enabled", "disabled", "missing"] as const;
const DELIVERY_COUNT_KEYS = ["pending", "sending", "retrying", "sent", "failed", "unknown"] as const;

export type EscalationPreviewSeverity = (typeof SEVERITIES)[number];
export type EscalationPreviewAlertStatus = (typeof ALERT_STATUSES)[number];
export type EscalationPreviewStatus = (typeof PREVIEW_STATUSES)[number];
export type EscalationPreviewPolicySource = (typeof POLICY_SOURCES)[number];
export type EscalationPreviewDeliveryDecision = (typeof DELIVERY_DECISIONS)[number];
export type EscalationPreviewWarning = (typeof WARNINGS)[number];
export type EscalationPreviewStepStatus = (typeof STEP_STATUSES)[number];
export type EscalationPreviewChannelState = (typeof CHANNEL_STATES)[number];

export type DraftEscalationScenario = {
  nodeId: number;
  errorCode: string;
  severity: EscalationPreviewSeverity;
  tags: string[];
  triggeredAt: string;
  evaluationAt: string;
};

export type EscalationPreviewChannel = {
  id: number;
  name: string | null;
  type: string | null;
  state: EscalationPreviewChannelState;
};

export type EscalationPreviewStep = {
  levelIndex: number;
  delaySeconds: number;
  dueAt: string;
  projectedAt: string | null;
  status: EscalationPreviewStepStatus;
  severityBefore: EscalationPreviewSeverity;
  severityAfter: EscalationPreviewSeverity;
  tagsAfter: string[];
  matchedSilenceIds: number[];
  channels: EscalationPreviewChannel[];
};

export type EscalationPreviewDeliveryCounts = Record<(typeof DELIVERY_COUNT_KEYS)[number], number>;

export type EscalationPreviewPolicy = {
  id: number | null;
  name: string;
  minSeverity: EscalationPreviewSeverity;
  enabled: boolean;
  updatedAt: string | null;
};

export type EscalationPreviewAlert = {
  id: number | null;
  nodeId: number;
  severity: EscalationPreviewSeverity;
  status: EscalationPreviewAlertStatus;
  triggeredAt: string;
  lastLevelFired: number;
  tags: string[];
};

export type EscalationPreviewResult = {
  mode: "draft" | "alert";
  basis: "current_snapshot";
  generatedAt: string;
  expiresAt: string;
  evaluationAt: string;
  tickSeconds: number;
  requestedAlertId: number | null;
  status: EscalationPreviewStatus;
  policySource: EscalationPreviewPolicySource;
  sourceResourceId: number | null;
  policy: EscalationPreviewPolicy | null;
  alert: EscalationPreviewAlert | null;
  nextLevelIndex: number | null;
  nextDueAt: string | null;
  nextDueNow: boolean | null;
  deliveryDecision: EscalationPreviewDeliveryDecision | null;
  deliveryCounts: EscalationPreviewDeliveryCounts | null;
  warnings: EscalationPreviewWarning[];
  steps: EscalationPreviewStep[];
};

const textEncoder = new TextEncoder();

export function getUtf8ByteLength(str: string): number {
  return textEncoder.encode(str).length;
}

/** Same set as Go unicode.IsControl: C0, DEL and C1 controls. */
export function hasControlCharacter(value: string): boolean {
  for (const char of value) {
    const code = char.codePointAt(0)!;
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) return true;
  }
  return false;
}

// ---- UTC timestamps ----

type UtcInstant = { epochSeconds: number; nanos: number };

const UTC_TIMESTAMP = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/;

function daysInMonth(year: number, month: number): number {
  if (month === 2) return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
}

/** Epoch seconds for calendar fields; setUTCFullYear avoids Date.UTC remapping years 0-99 to 19xx. */
export function utcEpochSeconds(year: number, month: number, day: number, hour: number, minute: number, second: number): number {
  const date = new Date(Date.UTC(2000, month - 1, day, hour, minute, second));
  date.setUTCFullYear(year, month - 1, day);
  return date.getTime() / 1000;
}

/**
 * Parses a server UTC timestamp (Go RFC3339Nano after .UTC(): trailing "Z", years 1-9999,
 * real calendar dates, no leap second). Offsets are rejected because the server only emits UTC.
 */
function parseUtcInstant(value: unknown): UtcInstant | null {
  if (typeof value !== "string") return null;
  const match = UTC_TIMESTAMP.exec(value);
  if (!match) return null;
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number);
  if (year < 1 || month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month)) return null;
  if (hour > 23 || minute > 59 || second > 59) return null;
  return {
    epochSeconds: utcEpochSeconds(year, month, day, hour, minute, second),
    nanos: Number((match[7] ?? "").padEnd(9, "0")),
  };
}

export function isUtcTimestamp(value: unknown): value is string {
  return parseUtcInstant(value) !== null;
}

function compareInstants(a: UtcInstant, b: UtcInstant): number {
  return a.epochSeconds - b.epochSeconds || a.nanos - b.nanos;
}

// ---- response readers ----

export class EscalationPreviewContractError extends Error {
  constructor(path: string, expectation: string) {
    super(`Invalid escalation preview response: ${path} ${expectation}`);
    this.name = "EscalationPreviewContractError";
  }
}

function check(condition: boolean, path: string, expectation: string): asserts condition {
  if (!condition) throw new EscalationPreviewContractError(path, expectation);
}

function readRecord(value: unknown, path: string): Record<string, unknown> {
  check(typeof value === "object" && value !== null && !Array.isArray(value), path, "must be an object");
  return value as Record<string, unknown>;
}

function readEnum<T extends string>(value: unknown, allowed: readonly T[], path: string): T {
  check(typeof value === "string" && (allowed as readonly string[]).includes(value), path, `must be one of ${allowed.join("|")}`);
  return value as T;
}

function readInteger(value: unknown, path: string, min: number, max = Number.MAX_SAFE_INTEGER): number {
  check(typeof value === "number" && Number.isSafeInteger(value) && value >= min && value <= max, path, `must be an integer in [${min}, ${max}]`);
  return value as number;
}

const readId = (value: unknown, path: string) => readInteger(value, path, 1);

function readNull(value: unknown, path: string): null {
  check(value === null, path, "must be null");
  return null;
}

function readBoolean(value: unknown, path: string): boolean {
  check(typeof value === "boolean", path, "must be a boolean");
  return value as boolean;
}

function readString(value: unknown, path: string): string {
  check(typeof value === "string", path, "must be a string");
  return value as string;
}

function readTime(value: unknown, path: string): { text: string; instant: UtcInstant } {
  const instant = parseUtcInstant(value);
  check(instant !== null, path, "must be a UTC RFC3339 timestamp");
  return { text: value as string, instant };
}

function readArray(value: unknown, path: string, max: number): unknown[] {
  check(Array.isArray(value) && value.length <= max, path, `must be an array of at most ${max} items`);
  return value as unknown[];
}

/** Persisted/projected tag unions are not count-bounded by contract; only element types are checked. */
function readStrings(value: unknown, path: string): string[] {
  check(Array.isArray(value), path, "must be an array");
  return (value as unknown[]).map((item, index) => readString(item, `${path}[${index}]`));
}

function readAscendingIds(value: unknown, path: string, max: number): number[] {
  const ids = readArray(value, path, max).map((item, index) => readId(item, `${path}[${index}]`));
  check(ids.every((id, index) => index === 0 || id > ids[index - 1]), path, "must be strictly ascending");
  return ids;
}

function readPolicy(value: unknown, mode: "draft" | "alert"): EscalationPreviewPolicy {
  const raw = readRecord(value, "policy");
  const name = readString(raw.name, "policy.name");
  const nameBytes = getUtf8ByteLength(name.trim());
  check(nameBytes >= 1 && nameBytes <= MAX_PREVIEW_NAME_BYTES, "policy.name", "must be 1-100 UTF-8 bytes");
  return {
    id: mode === "draft" ? readNull(raw.id, "policy.id") : readId(raw.id, "policy.id"),
    name,
    minSeverity: readEnum(raw.min_severity, SEVERITIES, "policy.min_severity"),
    enabled: readBoolean(raw.enabled, "policy.enabled"),
    updatedAt: mode === "draft" ? readNull(raw.updated_at, "policy.updated_at") : readTime(raw.updated_at, "policy.updated_at").text,
  };
}

function readAlert(value: unknown, mode: "draft" | "alert", requestedAlertId: number | null): EscalationPreviewAlert {
  const raw = readRecord(value, "alert");
  const alert: EscalationPreviewAlert = {
    id: mode === "draft" ? readNull(raw.id, "alert.id") : readId(raw.id, "alert.id"),
    nodeId: readInteger(raw.node_id, "alert.node_id", 0),
    severity: readEnum(raw.severity, SEVERITIES, "alert.severity"),
    status: readEnum(raw.status, ALERT_STATUSES, "alert.status"),
    triggeredAt: readTime(raw.triggered_at, "alert.triggered_at").text,
    lastLevelFired: readInteger(raw.last_level_fired, "alert.last_level_fired", -1),
    tags: readStrings(raw.tags, "alert.tags"),
  };
  check(alert.id === requestedAlertId, "alert.id", `must equal requested_alert_id ${String(requestedAlertId)}`);
  if (mode === "draft") {
    check(alert.status === "open" && alert.lastLevelFired === -1, "alert", "draft scenario must be open with last_level_fired=-1");
  }
  return alert;
}

function readChannels(value: unknown, path: string): EscalationPreviewChannel[] {
  const list = readArray(value, path, MAX_PREVIEW_CHANNELS_PER_LEVEL);
  check(list.length > 0, path, "must reference at least one channel");
  const seen = new Set<number>();
  return list.map((item, index) => {
    const itemPath = `${path}[${index}]`;
    const raw = readRecord(item, itemPath);
    const id = readId(raw.id, `${itemPath}.id`);
    check(!seen.has(id), `${itemPath}.id`, "must be unique");
    seen.add(id);
    const state = readEnum(raw.state, CHANNEL_STATES, `${itemPath}.state`);
    const missing = state === "missing";
    return {
      id,
      state,
      name: missing ? readNull(raw.name, `${itemPath}.name`) : readString(raw.name, `${itemPath}.name`),
      type: missing ? readNull(raw.type, `${itemPath}.type`) : readString(raw.type, `${itemPath}.type`),
    };
  });
}

function readSteps(value: unknown, nextLevelIndex: number, evaluation: UtcInstant): EscalationPreviewStep[] {
  const list = readArray(value, "steps", MAX_PREVIEW_LEVELS);
  check(list.length > 0 && nextLevelIndex + list.length <= MAX_PREVIEW_LEVELS, "steps", "must list the remaining 1-5 levels");
  let blocked = false;
  let previousDelay = -1;
  let previousProjected: UtcInstant | null = null;
  return list.map((item, index) => {
    const path = `steps[${index}]`;
    const raw = readRecord(item, path);
    check(raw.level_index === nextLevelIndex + index, `${path}.level_index`, "must continue from next_level_index");
    const delaySeconds = readInteger(raw.delay_seconds, `${path}.delay_seconds`, 0, MAX_PREVIEW_DELAY_SECONDS);
    check(delaySeconds > previousDelay, `${path}.delay_seconds`, "must strictly increase");
    previousDelay = delaySeconds;
    const due = readTime(raw.due_at, `${path}.due_at`);
    const status = readEnum(raw.status, STEP_STATUSES, `${path}.status`);
    const isBlocked = status === "blocked_minimum" || status === "blocked_prior";
    check(status === "blocked_prior" ? blocked : !blocked, `${path}.status`, "blocked levels must be one blocked_minimum followed only by blocked_prior");
    blocked = blocked || isBlocked;
    const step: EscalationPreviewStep = {
      levelIndex: nextLevelIndex + index,
      delaySeconds,
      dueAt: due.text,
      projectedAt: null,
      status,
      severityBefore: readEnum(raw.severity_before, SEVERITIES, `${path}.severity_before`),
      severityAfter: readEnum(raw.severity_after, SEVERITIES, `${path}.severity_after`),
      tagsAfter: readStrings(raw.tags_after, `${path}.tags_after`),
      matchedSilenceIds: readAscendingIds(raw.matched_silence_ids, `${path}.matched_silence_ids`, MAX_MATCHED_SILENCES),
      channels: readChannels(raw.channels, `${path}.channels`),
    };
    if (isBlocked) {
      readNull(raw.projected_at, `${path}.projected_at`);
      check(step.matchedSilenceIds.length === 0 && step.severityBefore === step.severityAfter, path, "blocked levels must not advance or match silences");
      return step;
    }
    const projected = readTime(raw.projected_at, `${path}.projected_at`);
    check(
      compareInstants(projected.instant, due.instant) >= 0 &&
        compareInstants(projected.instant, evaluation) >= 0 &&
        (previousProjected === null || compareInstants(projected.instant, previousProjected) > 0),
      `${path}.projected_at`,
      "must not precede due_at, evaluation_at, or the previous projected level"
    );
    previousProjected = projected.instant;
    const silenced = step.matchedSilenceIds.length > 0;
    const anyEnabled = step.channels.some((channel) => channel.state === "enabled");
    check(
      status === "silenced" ? silenced : !silenced && (status === "would_enqueue") === anyEnabled,
      `${path}.status`,
      "must agree with matched silences and enabled channels"
    );
    step.projectedAt = projected.text;
    return step;
  });
}

function readDeliveryCounts(value: unknown): EscalationPreviewDeliveryCounts {
  const raw = readRecord(value, "delivery_counts");
  const counts = {} as EscalationPreviewDeliveryCounts;
  for (const key of DELIVERY_COUNT_KEYS) counts[key] = readInteger(raw[key], `delivery_counts.${key}`, 0);
  return counts;
}

function readWarnings(value: unknown): EscalationPreviewWarning[] {
  const warnings = readArray(value, "warnings", WARNINGS.length).map((item, index) => readEnum(item, WARNINGS, `warnings[${index}]`));
  check(new Set(warnings).size === warnings.length, "warnings", "must be unique");
  return warnings;
}

function decodePreviewResult(raw: unknown, mode: "draft" | "alert", requestedAlertId: number | null): EscalationPreviewResult {
  const r = readRecord(raw, "response");
  check(r.mode === mode, "mode", `must be "${mode}"`);
  check(r.basis === "current_snapshot", "basis", 'must be "current_snapshot"');
  const generated = readTime(r.generated_at, "generated_at");
  const expires = readTime(r.expires_at, "expires_at");
  check(
    expires.instant.epochSeconds - generated.instant.epochSeconds === PREVIEW_TTL_MS / 1000 && expires.instant.nanos === generated.instant.nanos,
    "expires_at",
    "must equal generated_at plus 60 seconds"
  );
  const evaluation = readTime(r.evaluation_at, "evaluation_at");
  if (mode === "alert") {
    check(compareInstants(evaluation.instant, generated.instant) === 0, "evaluation_at", "must equal generated_at for alert diagnosis");
  }
  check(r.tick_seconds === PREVIEW_TICK_SECONDS, "tick_seconds", `must be ${PREVIEW_TICK_SECONDS}`);
  check(r.requested_alert_id === requestedAlertId, "requested_alert_id", `must equal ${String(requestedAlertId)}`);
  const status = readEnum(r.status, PREVIEW_STATUSES, "status");
  const warnings = readWarnings(r.warnings);

  const base = {
    mode,
    basis: "current_snapshot" as const,
    generatedAt: generated.text,
    expiresAt: expires.text,
    evaluationAt: evaluation.text,
    tickSeconds: PREVIEW_TICK_SECONDS,
    requestedAlertId,
    status,
    warnings,
  };

  if (status === "unsupported_state") {
    for (const key of ["source_resource_id", "policy", "alert", "next_level_index", "next_due_at", "next_due_now", "delivery_decision", "delivery_counts"]) {
      readNull(r[key], key);
    }
    check(r.policy_source === "none", "policy_source", 'must be "none" for unsupported_state');
    readArray(r.steps, "steps", 0);
    check(warnings.length > 0, "warnings", "must explain unsupported_state");
    return {
      ...base,
      policySource: "none",
      sourceResourceId: null,
      policy: null,
      alert: null,
      nextLevelIndex: null,
      nextDueAt: null,
      nextDueNow: null,
      deliveryDecision: null,
      deliveryCounts: null,
      steps: [],
    };
  }

  check(
    warnings.every((warning) => PROJECTABLE_WARNINGS.includes(warning)) && (mode === "alert" || warnings.length === 0),
    "warnings",
    "contains a code that only accompanies unsupported_state"
  );
  const policySource = readEnum(r.policy_source, POLICY_SOURCES, "policy_source");
  check((mode === "draft") === (policySource === "draft"), "policy_source", `is not valid for ${mode} mode`);
  const noSource = policySource === "draft" || policySource === "none";
  const sourceResourceId = noSource ? readNull(r.source_resource_id, "source_resource_id") : readId(r.source_resource_id, "source_resource_id");
  const policy = policySource === "none" ? readNull(r.policy, "policy") : readPolicy(r.policy, mode);
  const alert = readAlert(r.alert, mode, requestedAlertId);
  const deliveryDecision = mode === "draft" ? readNull(r.delivery_decision, "delivery_decision") : readEnum(r.delivery_decision, DELIVERY_DECISIONS, "delivery_decision");
  const deliveryCounts = mode === "draft" ? readNull(r.delivery_counts, "delivery_counts") : readDeliveryCounts(r.delivery_counts);

  check((alert.status !== "open") === (status === "not_open"), "status", "must be not_open exactly when the alert is not open");
  if (status !== "not_open") {
    check((status === "no_policy") === (policy === null), "status", "must be no_policy exactly when no policy is bound");
    check(policy === null || (status === "disabled") === !policy.enabled, "status", "must be disabled exactly when the policy is disabled");
  }

  if (status !== "projected") {
    for (const key of ["next_level_index", "next_due_at", "next_due_now"]) readNull(r[key], key);
    readArray(r.steps, "steps", 0);
    return {
      ...base,
      policySource,
      sourceResourceId,
      policy,
      alert,
      nextLevelIndex: null,
      nextDueAt: null,
      nextDueNow: null,
      deliveryDecision,
      deliveryCounts,
      steps: [],
    };
  }

  const nextLevelIndex = readInteger(r.next_level_index, "next_level_index", 0, MAX_PREVIEW_LEVELS - 1);
  check(nextLevelIndex === alert.lastLevelFired + 1, "next_level_index", "must follow alert.last_level_fired");
  const nextDue = readTime(r.next_due_at, "next_due_at");
  const nextDueNow = readBoolean(r.next_due_now, "next_due_now");
  check(nextDueNow === compareInstants(nextDue.instant, evaluation.instant) <= 0, "next_due_now", "must agree with next_due_at and evaluation_at");
  const steps = readSteps(r.steps, nextLevelIndex, evaluation.instant);
  check(steps[0].dueAt === nextDue.text, "next_due_at", "must equal the first step due_at");
  return {
    ...base,
    policySource,
    sourceResourceId,
    policy,
    alert,
    nextLevelIndex,
    nextDueAt: nextDue.text,
    nextDueNow,
    deliveryDecision,
    deliveryCounts,
    steps,
  };
}

// ---- draft request validation ----

function isPreviewTagList(tags: readonly string[]): boolean {
  return tags.length <= MAX_PREVIEW_DRAFT_TAGS && tags.every((tag) => typeof tag === "string" && getUtf8ByteLength(tag) <= MAX_PREVIEW_TAG_BYTES);
}

export function isPreviewErrorCode(code: string): boolean {
  const bytes = getUtf8ByteLength(code);
  return bytes >= 1 && bytes <= MAX_PREVIEW_ERROR_CODE_BYTES && !hasControlCharacter(code);
}

/** Mirrors ValidatePolicyInput plus the preview-only request bounds enforced by the handler. */
export function isPreviewablePolicy(policy: EscalationPolicyInput): boolean {
  const nameBytes = getUtf8ByteLength(policy.name.trim());
  const levels = policy.levels;
  return (
    nameBytes >= 1 &&
    nameBytes <= MAX_PREVIEW_NAME_BYTES &&
    getUtf8ByteLength(policy.description ?? "") <= MAX_PREVIEW_DESCRIPTION_BYTES &&
    (SEVERITIES as readonly string[]).includes(policy.minSeverity) &&
    levels.length >= 1 &&
    levels.length <= MAX_PREVIEW_LEVELS &&
    levels.every(
      (level, index) =>
        Number.isSafeInteger(level.delaySeconds) &&
        level.delaySeconds <= MAX_PREVIEW_DELAY_SECONDS &&
        (index === 0 ? level.delaySeconds === 0 : level.delaySeconds > levels[index - 1].delaySeconds) &&
        level.integrationIds.length >= 1 &&
        level.integrationIds.length <= MAX_PREVIEW_CHANNELS_PER_LEVEL &&
        level.integrationIds.every((id) => Number.isSafeInteger(id) && id > 0) &&
        (level.severityOverride === "" || (SEVERITIES as readonly string[]).includes(level.severityOverride)) &&
        isPreviewTagList(level.tags)
    )
  );
}

export function isPreviewableScenario(scenario: DraftEscalationScenario): boolean {
  return (
    Number.isSafeInteger(scenario.nodeId) &&
    scenario.nodeId >= 0 &&
    isPreviewErrorCode(scenario.errorCode) &&
    (SEVERITIES as readonly string[]).includes(scenario.severity) &&
    isPreviewTagList(scenario.tags) &&
    isUtcTimestamp(scenario.triggeredAt) &&
    isUtcTimestamp(scenario.evaluationAt)
  );
}

export async function previewEscalationDraft(
  token: string,
  input: { policy: EscalationPolicyInput; scenario: DraftEscalationScenario },
  options?: { signal?: AbortSignal }
): Promise<EscalationPreviewResult> {
  const { policy, scenario } = input;
  if (!isPreviewablePolicy(policy)) throw new Error("Invalid draft escalation policy for preview");
  if (!isPreviewableScenario(scenario)) throw new Error("Invalid draft escalation scenario for preview");

  const body = {
    policy: {
      name: policy.name.trim(),
      description: policy.description ?? "",
      min_severity: policy.minSeverity,
      enabled: policy.enabled,
      levels: policy.levels.map((level) => ({
        delay_seconds: level.delaySeconds,
        integration_ids: level.integrationIds,
        severity_override: level.severityOverride,
        tags: level.tags,
      })),
    },
    scenario: {
      node_id: scenario.nodeId,
      error_code: scenario.errorCode,
      severity: scenario.severity,
      tags: scenario.tags,
      triggered_at: scenario.triggeredAt,
      evaluation_at: scenario.evaluationAt,
    },
  };

  const raw = await request<unknown>("/escalation-policies/preview", {
    method: "POST",
    token,
    body,
    signal: options?.signal,
  });
  return decodePreviewResult(raw, "draft", null);
}

export async function previewAlertEscalation(
  token: string,
  alertId: number,
  options?: { signal?: AbortSignal }
): Promise<EscalationPreviewResult> {
  if (!Number.isSafeInteger(alertId) || alertId <= 0) {
    throw new Error(`Invalid alertId: expected positive integer, got ${alertId}`);
  }
  const raw = await request<unknown>(`/alerts/${alertId}/escalation-preview`, {
    method: "GET",
    token,
    signal: options?.signal,
  });
  return decodePreviewResult(raw, "alert", alertId);
}
