import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { ServerCog } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog, type DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { formatUtcOffset, parseDatetimeLocalValue, toDatetimeLocalValue } from "@/lib/date-utils";
import { getErrorMessage } from "@/lib/utils";
import {
  parseSSHKeyType,
  type NewNodeInput,
  type NodeRecord,
  type SSHKeyRecord,
  type SSHKeyType,
} from "@/types/domain";

type NodeEditorDraft = {
  id?: number;
  name: string;
  host: string;
  port: string;
  username: string;
  authType: "key" | "password";
  keyId: string;
  password: string;
  tags: string;
  basePath: string;
  backupDir: string;
  useSudo: boolean;
  inlineKeyName: string;
  inlineKeyType: SSHKeyType;
  inlinePrivateKey: string;
  maintenanceStart: string;
  maintenanceEnd: string;
};

type FieldKey =
  | "name"
  | "host"
  | "port"
  | "username"
  | "keyId"
  | "password"
  | "inlinePrivateKey"
  | "basePath"
  | "backupDir"
  | "maintenanceStart"
  | "maintenanceEnd";

type FieldErrors = Partial<Record<FieldKey, string>>;

type ActionError = { kind: "save" | "test"; message: string };

const NEW_KEY_OPTION = "__new__";

const emptyDraft: NodeEditorDraft = {
  name: "",
  host: "",
  port: "22",
  username: "root",
  authType: "key",
  keyId: "",
  password: "",
  tags: "",
  basePath: "/",
  backupDir: "",
  useSudo: false,
  inlineKeyName: "",
  inlineKeyType: "auto",
  inlinePrivateKey: "",
  maintenanceStart: "",
  maintenanceEnd: "",
};

function toAuthType(value: string): "key" | "password" {
  if (value === "password") {
    return "password";
  }
  return "key";
}

// 标签按服务端原文回显；仅当原文与已解析标签不一致（本地改过 tags）时退回拼接值。
function draftTags(node: NodeRecord): string {
  const raw = node.rawTags;
  if (raw !== undefined) {
    const parsed = raw.split(",").map((one) => one.trim()).filter(Boolean);
    if (parsed.length === node.tags.length && parsed.every((tag, index) => tag === node.tags[index])) {
      return raw;
    }
  }
  return node.tags.join(",");
}

function toDraft(node: NodeRecord): NodeEditorDraft {
  return {
    id: node.id,
    name: node.name,
    host: node.host,
    port: String(node.port),
    username: node.username,
    authType: node.authType,
    keyId: node.keyId ?? "",
    password: "",
    tags: draftTags(node),
    basePath: node.basePath ?? "",
    backupDir: node.backupDir || "",
    useSudo: node.useSudo ?? false,
    inlineKeyName: "",
    inlineKeyType: "auto",
    inlinePrivateKey: "",
    maintenanceStart: toDatetimeLocalValue(node.maintenanceStart),
    maintenanceEnd: toDatetimeLocalValue(node.maintenanceEnd),
  };
}

function sanitizeForBackupDir(name: string): string {
  let s = name.toLowerCase();
  s = s.replace(/[^a-z0-9\-_.]/g, "-");
  s = s.replace(/-{2,}/g, "-");
  s = s.replace(/^-+|-+$/g, "");
  return s.length < 2 ? "" : s;
}

function hasReservedPathChars(value: string): boolean {
  return value.includes("/") || value.includes("\\") || value.includes("..") || value.includes("\0");
}

// 与服务端 strings.TrimSpace 的判定一致（Unicode White_Space：含 NEL、不含 BOM，不同于 String#trim）。
// 备份目录标识：空白即“未填写”，新建按节点名称生成、编辑保留原标识；基础路径：仅含空白的旧值运行时等同未设置。
function isGoBlank(value: string): boolean {
  return /^\p{White_Space}*$/u.test(value);
}

// 与服务端 node.ValidateBasePath 一致：非空值须为以 / 开头的 POSIX 绝对路径，首尾无 Go 空白，
// 不含空字符、孤立代理项（无法编码为合法 UTF-8）或完整的 .. 路径段；其余写法（/、. 前缀、内部空格、
// 重复斜杠、末尾斜杠）按原样保存。空串不在此校验：新建使用默认 /，编辑保留原值。
function basePathErrorKey(value: string): string | null {
  if (value.includes("\0") || /[\uD800-\uDFFF]/u.test(value)) return "nodeEditor.validation.basePathInvalidChars";
  if (/^\p{White_Space}|\p{White_Space}$/u.test(value)) return "nodeEditor.validation.basePathEdgeWhitespace";
  if (!value.startsWith("/")) return "nodeEditor.validation.basePathNotAbsolute";
  if (value.split("/").includes("..")) return "nodeEditor.validation.basePathParentSegment";
  return null;
}

function parsePort(raw: string): number | null {
  const trimmed = raw.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const port = Number(trimmed);
  return port >= 1 && port <= 65535 ? port : null;
}

// 本地时间 → RFC3339；空串表示清除。调用方先校验，非法值不会到达这里。
function toInstantString(value: string): string {
  if (!value.trim()) return "";
  return parseDatetimeLocalValue(value)?.toISOString() ?? "";
}

// 微秒精度的时刻：ms 为整毫秒，micros 为毫秒内的 0–999 微秒（Date 只到毫秒）。
type Instant = { ms: number; micros: number };

const RFC3339_PARTS = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})$/;

// 服务端存储值按原偏移解析并截断到微秒，与服务端的比较精度一致；非 RFC3339 旧值退回 Date 解析。
function parseStoredInstant(raw: string): Instant | null {
  const match = RFC3339_PARTS.exec(raw);
  if (!match) {
    const ms = new Date(raw).getTime();
    return Number.isNaN(ms) ? null : { ms, micros: 0 };
  }
  const [, wholeSeconds, fraction = "", zone] = match;
  const ms = Date.parse(`${wholeSeconds}${zone}`);
  if (Number.isNaN(ms)) return null;
  const digits = fraction.slice(0, 6).padEnd(6, "0");
  return { ms: ms + Number(digits.slice(0, 3)), micros: Number(digits.slice(3)) };
}

function isSSHKeyExpired(key: SSHKeyRecord, nowMs: number): boolean {
  if (!key.expiresAt) return false;
  const expiry = parseDatetimeLocalValue(key.expiresAt) ?? new Date(key.expiresAt);
  return !Number.isNaN(expiry.getTime()) && expiry.getTime() <= nowMs;
}

function describedBy(...ids: Array<string | false | null | undefined>): string | undefined {
  const present = ids.filter((id): id is string => Boolean(id));
  return present.length > 0 ? present.join(" ") : undefined;
}

function hasConnectionChanges(draft: NodeEditorDraft, baseline: NodeEditorDraft): boolean {
  return (
    draft.host !== baseline.host ||
    draft.port !== baseline.port ||
    draft.username !== baseline.username ||
    draft.authType !== baseline.authType ||
    draft.keyId !== baseline.keyId ||
    draft.password !== "" ||
    draft.inlinePrivateKey !== ""
  );
}

type EditorContext = {
  draft: NodeEditorDraft;
  baseline: NodeEditorDraft;
  editingNode: NodeRecord | null;
};

// 编辑时只校验用户改过的格式；旧数据中未触及的字段原样交给服务端，避免为无关修复阻塞保存。
function validateDraft({ draft, baseline, editingNode }: EditorContext, t: TFunction): FieldErrors {
  const errors: FieldErrors = {};
  const isEditing = editingNode !== null;
  const changed = (key: keyof NodeEditorDraft) => !isEditing || draft[key] !== baseline[key];

  if (!draft.name.trim()) {
    errors.name = t("nodeEditor.validation.nameRequired");
  } else if (changed("name") && hasReservedPathChars(draft.name)) {
    errors.name = t("nodeEditor.validation.nameInvalid");
  }

  if (!draft.host.trim()) {
    errors.host = t("nodeEditor.validation.hostRequired");
  }

  if (changed("port") && parsePort(draft.port) === null) {
    errors.port = t("nodeEditor.validation.portInvalid");
  }

  if (!draft.username.trim()) {
    errors.username = t("nodeEditor.validation.usernameRequired");
  }

  if (draft.authType === "password") {
    const storedPassword = isEditing && baseline.authType === "password";
    if (!storedPassword && draft.password === "") {
      errors.password = t("nodeEditor.validation.passwordRequired");
    }
  } else if (draft.keyId === NEW_KEY_OPTION) {
    if (!draft.inlinePrivateKey.trim()) {
      errors.inlinePrivateKey = t("nodeEditor.validation.inlinePrivateKeyRequired");
    }
  } else if (!draft.keyId) {
    const storedInlineKey = isEditing && baseline.authType === "key" && baseline.keyId === "";
    if (!storedInlineKey) {
      errors.keyId = t("nodeEditor.validation.sshKeyRequired");
    }
  }

  // 编辑时与存储原值完全相同即原样回传（服务端保留且不复验），历史值不阻塞无关修改。
  if (draft.basePath !== "" && changed("basePath")) {
    const basePathError = basePathErrorKey(draft.basePath);
    if (basePathError) errors.basePath = t(basePathError);
  }

  // 非空值按原样存储，校验也针对原值（与服务端一致按字节计长度）。
  if (!isGoBlank(draft.backupDir)) {
    if (
      changed("backupDir") &&
      (new TextEncoder().encode(draft.backupDir).length > 128 || hasReservedPathChars(draft.backupDir))
    ) {
      errors.backupDir = t("nodeEditor.validation.backupDirInvalid");
    }
  } else if (isEditing) {
    // 留空即保留原值；只有原值本身为空（而非仅含空白的旧标识）时才无可保留。
    if (baseline.backupDir === "") {
      errors.backupDir = t("nodeEditor.validation.backupDirMissing");
    }
  } else if (sanitizeForBackupDir(draft.name) === "") {
    errors.backupDir = t("nodeEditor.validation.backupDirRequired");
  }

  if (changed("maintenanceStart") || changed("maintenanceEnd")) {
    // 未改动的一端用存储原值（保留偏移、夏令时重复时刻与微秒），改动的一端按本地分钟解析。
    const resolve = (key: "maintenanceStart" | "maintenanceEnd"): { present: boolean; instant: Instant | null } => {
      const value = draft[key];
      if (!value.trim()) return { present: false, instant: null };
      const stored = editingNode?.[key];
      if (!changed(key) && stored) {
        const original = parseStoredInstant(stored);
        if (original) return { present: true, instant: original };
      }
      const parsed = parseDatetimeLocalValue(value);
      if (!parsed) errors[key] = t("nodeEditor.validation.datetimeInvalid");
      return { present: true, instant: parsed && { ms: parsed.getTime(), micros: 0 } };
    };
    const start = resolve("maintenanceStart");
    const end = resolve("maintenanceEnd");
    if (start.present !== end.present) {
      errors[start.present ? "maintenanceEnd" : "maintenanceStart"] = t("nodeEditor.validation.maintenancePairRequired");
    } else if (
      start.instant &&
      end.instant &&
      (end.instant.ms < start.instant.ms ||
        (end.instant.ms === start.instant.ms && end.instant.micros <= start.instant.micros))
    ) {
      errors.maintenanceEnd = t("nodeEditor.validation.maintenanceEndAfterStart");
    }
  }

  return errors;
}

function buildNodeInput({ draft, baseline, editingNode }: EditorContext): NewNodeInput {
  const isEditing = editingNode !== null;
  const useInlineKey = draft.authType === "key" && draft.keyId === NEW_KEY_OPTION;
  return {
    name: draft.name.trim(),
    host: draft.host.trim(),
    port: parsePort(draft.port) ?? Number(baseline.port),
    username: draft.username.trim(),
    authType: draft.authType,
    keyId:
      draft.authType === "key" && !useInlineKey ? draft.keyId || null : null,
    password: draft.authType === "password" ? draft.password : undefined,
    tags: draft.tags,
    basePath: isEditing && (draft.basePath === "" || draft.basePath === baseline.basePath)
      ? undefined
      : draft.basePath,
    backupDir: draft.backupDir,
    useSudo: draft.useSudo,
    inlineKeyName: useInlineKey ? draft.inlineKeyName : undefined,
    inlineKeyType: useInlineKey ? draft.inlineKeyType : undefined,
    inlinePrivateKey: useInlineKey ? draft.inlinePrivateKey : undefined,
    // 编辑时未改动的维护时间直接省略，服务端保留原值（含秒与夏令时重复时刻）；空串表示清除。
    maintenanceStart:
      isEditing && draft.maintenanceStart === baseline.maintenanceStart
        ? undefined
        : toInstantString(draft.maintenanceStart),
    maintenanceEnd:
      isEditing && draft.maintenanceEnd === baseline.maintenanceEnd
        ? undefined
        : toInstantString(draft.maintenanceEnd),
  };
}

// 提交校验失败时按表单顺序聚焦第一个无效字段。
const FIELD_IDS: Record<FieldKey, string> = {
  name: "node-edit-name",
  host: "node-edit-host",
  port: "node-edit-port",
  username: "node-edit-username",
  keyId: "node-edit-ssh-key",
  password: "node-edit-password",
  inlinePrivateKey: "node-edit-inline-private-key",
  basePath: "node-edit-base-path",
  backupDir: "node-edit-backup-dir",
  maintenanceStart: "node-edit-maint-start",
  maintenanceEnd: "node-edit-maint-end",
};
const FIELD_ORDER = Object.keys(FIELD_IDS) as FieldKey[];

const labelClass = "mb-1 block text-sm font-medium";
const hintClass = "mt-1 text-xs text-muted-foreground";
const warningClass = "mt-1 text-xs text-[hsl(var(--warning-text))]";

function FieldError({ id, message }: { id: string; message?: string }) {
  if (!message) return null;
  return (
    <p id={id} role="alert" className="mt-1 text-xs text-destructive">
      {message}
    </p>
  );
}

function EditorSection({ legend, disabled, children }: { legend: string; disabled: boolean; children: ReactNode }) {
  return (
    <div className="border-t border-border/70 pt-3 first:border-t-0 first:pt-0">
      <fieldset disabled={disabled} className="min-w-0 space-y-3">
        <legend className="text-xs font-semibold text-muted-foreground">{legend}</legend>
        {children}
      </fieldset>
    </div>
  );
}

type NodeEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editingNode?: NodeRecord | null;
  sshKeys: SSHKeyRecord[];
  /**
   * onSave 在节点写入落定时结算（成功后的连接测试由页面另行发起）。
   * 内联新建密钥成功后、写节点之前，页面调用 onKeyCreated；若随后写节点失败，
   * 编辑器改为绑定这把已创建的密钥，重试时不会再次创建同名密钥。
   */
  onSave: (input: NewNodeInput, nodeId: number | undefined, onKeyCreated: (keyId: string) => void) => Promise<void>;
  onTestConnection?: (nodeId: number) => Promise<void>;
  onCloseAutoFocus?: DialogCloseAutoFocus;
};

type CreatedKey = { id: string; name: string };

export function NodeEditorDialog({
  open,
  onOpenChange,
  editingNode,
  sshKeys,
  onSave,
  onTestConnection,
  onCloseAutoFocus,
}: NodeEditorDialogProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useDialogDraft<NodeEditorDraft, NodeRecord>(open, emptyDraft, editingNode, toDraft);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [backupDirManuallyEdited, setBackupDirManuallyEdited] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [actionError, setActionError] = useState<ActionError | null>(null);
  // 本次打开期间内联新建成功、但节点写入失败的密钥；列表尚未刷新出它时用于兜底显示。
  const [createdKey, setCreatedKey] = useState<CreatedKey | null>(null);

  // 每次打开（含关闭、鉴权重置导致的关闭、切换编辑对象）都是新的会话编号。
  // 异步保存/测试只在自己的会话仍有效时回写状态，旧会话的结果、锁与密钥回调都不会落到下一次打开。
  const editingNodeId = editingNode?.id ?? null;
  const [editorSession, setEditorSession] = useState({ id: 0, open, nodeId: editingNodeId });
  if (editorSession.open !== open || editorSession.nodeId !== editingNodeId) {
    setEditorSession({ id: editorSession.id + 1, open, nodeId: editingNodeId });
    setFieldErrors({});
    setActionError(null);
    setSaving(false);
    setTesting(false);
    setCreatedKey(null);
  }
  const sessionRef = useRef(editorSession.id);
  useLayoutEffect(() => {
    sessionRef.current = editorSession.id;
  }, [editorSession.id]);

  // 锁记录持有者的会话编号；只有当前会话持有的锁才算数。
  const savingLockRef = useRef<number | null>(null);
  const testingLockRef = useRef<number | null>(null);
  const actionErrorRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (actionError) {
      actionErrorRef.current?.scrollIntoView?.({ block: "nearest" });
    }
  }, [actionError]);

  const baseline = useMemo(() => (editingNode ? toDraft(editingNode) : emptyDraft), [editingNode]);
  const context: EditorContext = { draft, baseline, editingNode: editingNode ?? null };
  const isEditing = Boolean(draft.id);

  // 对话框关闭后重置标记
  if (!open && backupDirManuallyEdited) {
    setBackupDirManuallyEdited(false);
  }

  const busy = saving || testing;
  const connectionDirty = isEditing && hasConnectionChanges(draft, baseline);
  const storedInlineKey = isEditing && baseline.authType === "key" && baseline.keyId === "";
  const storedPassword = isEditing && baseline.authType === "password";
  const boundKeyMissing =
    isEditing && baseline.keyId !== "" && !sshKeys.some((key) => key.id === baseline.keyId);
  const createdKeyMissing =
    createdKey !== null &&
    createdKey.id !== baseline.keyId &&
    !sshKeys.some((key) => key.id === createdKey.id);

  // eslint-disable-next-line react-hooks/purity -- key expiry is judged against the current render time
  const nowMs = Date.now();
  const selectedKey =
    draft.authType === "key" && draft.keyId && draft.keyId !== NEW_KEY_OPTION
      ? sshKeys.find((key) => key.id === draft.keyId)
      : undefined;
  const selectedKeyWarning = selectedKey?.disabled
    ? t("nodeEditor.sshKeyDisabledWarning")
    : selectedKey && isSSHKeyExpired(selectedKey, nowMs)
      ? t("nodeEditor.sshKeyExpiredWarning")
      : null;

  const timeZoneName = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const timeZoneLabel = timeZoneName ? `${timeZoneName}, ${formatUtcOffset()}` : formatUtcOffset();

  // 空值或未改动的原值都沿用存储值；运行时按 Go TrimSpace 判定，仅含空白的旧值等同未设置根目录。
  const basePathKeepsStored = isEditing && (draft.basePath === "" || draft.basePath === baseline.basePath);
  const basePathNote = basePathKeepsStored
    ? baseline.basePath === ""
      ? t("nodeEditor.basePathBlankCurrent")
      : isGoBlank(baseline.basePath)
        ? t("nodeEditor.basePathWhitespaceLegacy")
        : draft.basePath === ""
          ? t("nodeEditor.basePathBlankKeeps", { path: baseline.basePath })
          : null
    : !isEditing && draft.basePath === ""
      ? t("nodeEditor.basePathBlankCurrent")
      : null;

  // 空白输入会保留原标识，只有明确填写不同的值才提示。
  const backupDirChanged =
    isEditing &&
    !isGoBlank(draft.backupDir) &&
    draft.backupDir !== baseline.backupDir;
  // eslint-disable-next-line no-control-regex -- intentional: detect non-ASCII chars in node name
  const showBackupDirNonAsciiWarning = !isEditing && !backupDirManuallyEdited && /[^\x00-\x7F]/.test(draft.name);

  const updateDraft = (patch: Partial<NodeEditorDraft>, ...clear: FieldKey[]) => {
    setDraft((prev) => ({ ...prev, ...patch }));
    if (clear.length > 0) {
      setFieldErrors((prev) => {
        if (!clear.some((key) => prev[key])) return prev;
        const next = { ...prev };
        for (const key of clear) delete next[key];
        return next;
      });
    }
  };

  const handleSave = async () => {
    const session = sessionRef.current;
    if (savingLockRef.current === session || testingLockRef.current === session) {
      return;
    }
    const errors = validateDraft(context, t);
    setFieldErrors(errors);
    const firstInvalid = FIELD_ORDER.find((key) => errors[key]);
    if (firstInvalid) {
      document.getElementById(FIELD_IDS[firstInvalid])?.focus();
      return;
    }

    const isCurrent = () => sessionRef.current === session;
    const input = buildNodeInput(context);
    const pendingKeyName = draft.inlineKeyName.trim() || `${input.name}-key`;
    // 密钥已创建但节点写入失败时，改绑这把密钥并清掉内联字段，其余草稿保持不变。
    const onKeyCreated = (keyId: string) => {
      if (!isCurrent() || savingLockRef.current !== session || !keyId) return;
      setCreatedKey({ id: keyId, name: pendingKeyName });
      setDraft((prev) => ({
        ...prev,
        keyId,
        inlineKeyName: "",
        inlineKeyType: "auto",
        inlinePrivateKey: "",
      }));
    };

    savingLockRef.current = session;
    setActionError(null);
    setSaving(true);
    try {
      await onSave(input, draft.id, onKeyCreated);
    } catch (error) {
      if (isCurrent()) {
        setActionError({ kind: "save", message: getErrorMessage(error) });
      }
    } finally {
      if (savingLockRef.current === session) {
        savingLockRef.current = null;
      }
      if (isCurrent()) {
        setSaving(false);
      }
    }
  };

  // 测试的是已保存的配置；表单有未保存的连接改动时仍可测试，只提示两者的区别。
  const handleTestConnection = async () => {
    const session = sessionRef.current;
    if (!draft.id || !onTestConnection || savingLockRef.current === session || testingLockRef.current === session) {
      return;
    }
    testingLockRef.current = session;
    setActionError(null);
    setTesting(true);
    try {
      await onTestConnection(draft.id);
    } catch (error) {
      if (sessionRef.current === session) {
        setActionError({ kind: "test", message: getErrorMessage(error) });
      }
    } finally {
      if (testingLockRef.current === session) {
        testingLockRef.current = null;
      }
      if (sessionRef.current === session) {
        setTesting(false);
      }
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      onCloseAutoFocus={onCloseAutoFocus}
      size="lg"
      icon={<ServerCog className="size-5 text-primary" />}
      title={isEditing ? t('nodeEditor.titleEdit', { name: draft.name }) : t('nodeEditor.titleCreate')}
      description={isEditing
        ? t('nodeEditor.descEdit')
        : t('nodeEditor.descCreate')}
      saving={saving}
      lockWhileSaving
      submitDisabled={testing}
      onSubmit={handleSave}
      submitLabel={t('nodeEditor.submitLabel')}
      extraFooter={isEditing ? (
        <>
          <Button
            type="button"
            variant="outline"
            loading={testing}
            onClick={() => void handleTestConnection()}
            disabled={busy}
            aria-describedby={connectionDirty ? "node-edit-test-dirty-hint" : undefined}
          >
            {testing ? t('nodeEditor.testingConnection') : t('nodeEditor.testConnection')}
          </Button>
          {connectionDirty ? (
            <p id="node-edit-test-dirty-hint" className="order-first basis-full text-xs text-muted-foreground sm:text-right">
              {t('nodeEditor.testConnectionDirtyHint')}
            </p>
          ) : null}
        </>
      ) : null}
    >
      {actionError ? (
        <div ref={actionErrorRef}>
          <InlineAlert
            tone="critical"
            title={actionError.kind === "save" ? t('nodeEditor.saveErrorTitle') : t('nodeEditor.testErrorTitle')}
          >
            {actionError.message}
          </InlineAlert>
        </div>
      ) : null}

      <EditorSection legend={t('nodeEditor.sectionConnection')} disabled={saving}>
        <div>
          <label htmlFor="node-edit-name" className={labelClass}>{t('nodeEditor.nodeName')}</label>
          <Input id="node-edit-name" placeholder={t('nodeEditor.namePlaceholder')}
            value={draft.name}
            aria-invalid={Boolean(fieldErrors.name)}
            aria-describedby={describedBy(fieldErrors.name && "node-edit-name-error")}
            onChange={(event) => {
              const newName = event.target.value;
              if (!isEditing && !backupDirManuallyEdited) {
                updateDraft({ name: newName, backupDir: sanitizeForBackupDir(newName) }, "name", "backupDir");
              } else {
                updateDraft({ name: newName }, "name");
              }
            }}
          />
          <FieldError id="node-edit-name-error" message={fieldErrors.name} />
        </div>

        <div className="grid gap-3 md:grid-cols-[2fr_1fr]">
          <div>
            <label htmlFor="node-edit-host" className={labelClass}>{t('nodeEditor.host')}</label>
            <Input id="node-edit-host" placeholder="10.10.0.11"
              value={draft.host}
              aria-invalid={Boolean(fieldErrors.host)}
              aria-describedby={describedBy(fieldErrors.host && "node-edit-host-error")}
              onChange={(event) => updateDraft({ host: event.target.value }, "host")}
            />
            <FieldError id="node-edit-host-error" message={fieldErrors.host} />
          </div>
          <div>
            <label htmlFor="node-edit-port" className={labelClass}>{t('nodeEditor.port')}</label>
            <Input id="node-edit-port" type="text" inputMode="numeric"
              placeholder="22"
              value={draft.port}
              aria-invalid={Boolean(fieldErrors.port)}
              aria-describedby={describedBy(fieldErrors.port && "node-edit-port-error")}
              onChange={(event) => updateDraft({ port: event.target.value }, "port")}
            />
            <FieldError id="node-edit-port-error" message={fieldErrors.port} />
          </div>
        </div>

        <div>
          <label htmlFor="node-edit-username" className={labelClass}>{t('nodeEditor.sshUsername')}</label>
          <Input id="node-edit-username" placeholder="root"
            value={draft.username}
            aria-invalid={Boolean(fieldErrors.username)}
            aria-describedby={describedBy(fieldErrors.username && "node-edit-username-error")}
            onChange={(event) => {
              const newUsername = event.target.value;
              const isRoot = newUsername.trim() === "" || newUsername.trim() === "root";
              updateDraft({ username: newUsername, ...(isRoot ? { useSudo: false } : {}) }, "username");
            }}
          />
          <FieldError id="node-edit-username-error" message={fieldErrors.username} />
        </div>

        {draft.username.trim() !== "" && draft.username.trim() !== "root" && (
          <div className="flex items-start gap-2 text-sm">
            <input
              id="node-edit-use-sudo"
              type="checkbox"
              className="mt-0.5 size-4 shrink-0 accent-[hsl(var(--primary))]"
              checked={draft.useSudo}
              aria-describedby="node-edit-use-sudo-hint"
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, useSudo: event.target.checked }))
              }
            />
            <div className="min-w-0">
              <label htmlFor="node-edit-use-sudo" className="font-medium">{t('nodeEditor.useSudo')}</label>
              <p id="node-edit-use-sudo-hint" className="text-xs text-muted-foreground">{t('nodeEditor.useSudoHint')}</p>
            </div>
          </div>
        )}
      </EditorSection>

      <EditorSection legend={t('nodeEditor.sectionAuth')} disabled={saving}>
        <div>
          <label htmlFor="node-edit-auth" className={labelClass}>{t('nodeEditor.authMethod')}</label>
          <Select id="node-edit-auth" containerClassName="w-full"
            value={draft.authType}
            onChange={(event) =>
              updateDraft({ authType: toAuthType(event.target.value) }, "keyId", "password", "inlinePrivateKey")
            }
          >
            <option value="key">{t('nodeEditor.keyAuth')}</option>
            <option value="password">{t('nodeEditor.passwordAuth')}</option>
          </Select>
        </div>

        {draft.authType === "key" ? (
          <div>
            <label htmlFor="node-edit-ssh-key" className={labelClass}>{t('nodeEditor.sshKey')}</label>
            <Select id="node-edit-ssh-key" containerClassName="w-full"
              value={draft.keyId}
              aria-invalid={Boolean(fieldErrors.keyId)}
              aria-describedby={describedBy(
                fieldErrors.keyId && "node-edit-ssh-key-error",
                selectedKeyWarning && "node-edit-ssh-key-warning",
              )}
              onChange={(event) => updateDraft({ keyId: event.target.value }, "keyId", "inlinePrivateKey")}
            >
              <option value="">
                {storedInlineKey ? t('nodeEditor.keepCurrentInlineKey') : t('nodeEditor.selectExistingKey')}
              </option>
              {boundKeyMissing ? (
                <option value={baseline.keyId}>{t('nodeEditor.boundKeyUnavailable')}</option>
              ) : null}
              {createdKeyMissing ? (
                <option value={createdKey.id}>{createdKey.name}</option>
              ) : null}
              {sshKeys.map((key) => {
                const badge = key.disabled
                  ? t('sshKeys.scopeDisabledBadge')
                  : isSSHKeyExpired(key, nowMs)
                    ? t('nodeEditor.sshKeyExpiredTag')
                    : null;
                return (
                  <option key={key.id} value={key.id}>
                    {key.name} ({key.username}){badge ? ` · ${badge}` : ""}
                  </option>
                );
              })}
              <option value={NEW_KEY_OPTION}>{t('nodeEditor.newSshKey')}</option>
            </Select>
            <FieldError id="node-edit-ssh-key-error" message={fieldErrors.keyId} />
            {selectedKeyWarning ? (
              <p id="node-edit-ssh-key-warning" className={warningClass}>{selectedKeyWarning}</p>
            ) : null}
          </div>
        ) : (
          <div>
            <label htmlFor="node-edit-password" className={labelClass}>{t('nodeEditor.sshPassword')}</label>
            <Input id="node-edit-password" name="ssh-password" type="password" autoComplete="off"
              placeholder={storedPassword ? t('nodeEditor.passwordPlaceholderEdit') : t('nodeEditor.passwordPlaceholder')}
              value={draft.password}
              aria-invalid={Boolean(fieldErrors.password)}
              aria-describedby={describedBy(fieldErrors.password && "node-edit-password-error")}
              onChange={(event) => updateDraft({ password: event.target.value }, "password")}
            />
            <FieldError id="node-edit-password-error" message={fieldErrors.password} />
          </div>
        )}

        {draft.authType === "key" && draft.keyId === NEW_KEY_OPTION ? (
          <div className="space-y-2 rounded-md border border-dashed border-border bg-muted/40 p-3">
            <label htmlFor="node-edit-inline-private-key" className="block text-xs font-medium text-muted-foreground">
              {t('nodeEditor.inlineNewKey')}
            </label>
            <div className="grid gap-2 md:grid-cols-2">
              <div>
                <label htmlFor="node-edit-inline-key-name" className="mb-1 block text-xs font-medium">
                  {t('nodeEditor.keyName')}
                </label>
                <Input
                  id="node-edit-inline-key-name"
                  placeholder={t('nodeEditor.newKeyName')}
                  value={draft.inlineKeyName}
                  onChange={(event) => updateDraft({ inlineKeyName: event.target.value })}
                />
              </div>
              <div>
                <label htmlFor="node-edit-inline-key-type" className="mb-1 block text-xs font-medium">
                  {t('nodeEditor.keyType')}
                </label>
                <Select
                  id="node-edit-inline-key-type"
                  containerClassName="w-full"
                  value={draft.inlineKeyType}
                  onChange={(event) => updateDraft({ inlineKeyType: parseSSHKeyType(event.target.value) })}
                >
                  <option value="auto">{t('nodeEditor.autoDetect')}</option>
                  <option value="rsa">RSA</option>
                  <option value="ed25519">ED25519</option>
                  <option value="ecdsa">ECDSA</option>
                </Select>
              </div>
            </div>
            <Textarea
              id="node-edit-inline-private-key"
              className="mt-1 min-h-28 font-mono text-xs"
              placeholder={t('nodeEditor.privateKeyPlaceholder')}
              value={draft.inlinePrivateKey}
              aria-invalid={Boolean(fieldErrors.inlinePrivateKey)}
              aria-describedby={describedBy(fieldErrors.inlinePrivateKey && "node-edit-inline-private-key-error")}
              onChange={(event) => updateDraft({ inlinePrivateKey: event.target.value }, "inlinePrivateKey")}
            />
            <FieldError id="node-edit-inline-private-key-error" message={fieldErrors.inlinePrivateKey} />
          </div>
        ) : null}
      </EditorSection>

      <EditorSection legend={t('nodeEditor.sectionPaths')} disabled={saving}>
        <div>
          <label htmlFor="node-edit-base-path" className={labelClass}>{t('nodeEditor.basePath')}</label>
          <Input id="node-edit-base-path" placeholder="/"
            value={draft.basePath}
            aria-invalid={Boolean(fieldErrors.basePath)}
            aria-describedby={describedBy(
              fieldErrors.basePath && "node-edit-base-path-error",
              "node-edit-base-path-hint",
              basePathNote && "node-edit-base-path-note",
            )}
            onChange={(event) => updateDraft({ basePath: event.target.value }, "basePath")}
          />
          <FieldError id="node-edit-base-path-error" message={fieldErrors.basePath} />
          <p id="node-edit-base-path-hint" className={hintClass}>{t('nodeEditor.basePathHint')}</p>
          {basePathNote ? (
            <p id="node-edit-base-path-note" className={warningClass}>{basePathNote}</p>
          ) : null}
        </div>

        <div>
          <label htmlFor="node-edit-backup-dir" className={labelClass}>
            {t('nodeEditor.backupDir')}
          </label>
          <Input
            id="node-edit-backup-dir"
            placeholder={t('nodeEditor.backupDirPlaceholder')}
            value={draft.backupDir}
            aria-invalid={Boolean(fieldErrors.backupDir)}
            aria-describedby={describedBy(
              fieldErrors.backupDir && "node-edit-backup-dir-error",
              "node-edit-backup-dir-hint",
              backupDirChanged && "node-edit-backup-dir-warning",
              showBackupDirNonAsciiWarning && "node-edit-backup-dir-name-warning",
            )}
            onChange={(event) => {
              setBackupDirManuallyEdited(true);
              updateDraft({ backupDir: event.target.value }, "backupDir");
            }}
          />
          <FieldError id="node-edit-backup-dir-error" message={fieldErrors.backupDir} />
          <p id="node-edit-backup-dir-hint" className={hintClass}>
            {t(
              !isEditing
                ? 'nodeEditor.backupDirHintCreate'
                : baseline.backupDir === ""
                  ? 'nodeEditor.backupDirHintMissing'
                  : 'nodeEditor.backupDirHintEdit',
            )}
          </p>
          {backupDirChanged && (
            <p id="node-edit-backup-dir-warning" className={warningClass}>
              {t('nodeEditor.backupDirChangeWarning')}
            </p>
          )}
          {showBackupDirNonAsciiWarning && (
            <p id="node-edit-backup-dir-name-warning" className={warningClass}>
              {t('nodeEditor.backupDirNonAsciiWarning')}
            </p>
          )}
        </div>
      </EditorSection>

      <EditorSection legend={t('nodeEditor.sectionSchedule')} disabled={saving}>
        <div>
          <label htmlFor="node-edit-tags" className={labelClass}>{t('nodeEditor.tags')}</label>
          <Input id="node-edit-tags" placeholder={t('nodeEditor.tagsPlaceholder')}
            value={draft.tags}
            onChange={(event) => updateDraft({ tags: event.target.value })}
          />
        </div>

        <div className="grid gap-3 md:grid-cols-2">
          <div>
            <label htmlFor="node-edit-maint-start" className={labelClass}>{t('nodeEditor.maintenanceStart')}</label>
            <Input id="node-edit-maint-start" type="datetime-local"
              aria-invalid={Boolean(fieldErrors.maintenanceStart)}
              aria-describedby={describedBy(fieldErrors.maintenanceStart && "node-edit-maint-start-error", "node-edit-maint-hint")}
              value={draft.maintenanceStart}
              onChange={(event) => updateDraft({ maintenanceStart: event.target.value }, "maintenanceStart", "maintenanceEnd")}
            />
            <FieldError id="node-edit-maint-start-error" message={fieldErrors.maintenanceStart} />
          </div>
          <div>
            <label htmlFor="node-edit-maint-end" className={labelClass}>{t('nodeEditor.maintenanceEnd')}</label>
            <Input id="node-edit-maint-end" type="datetime-local"
              aria-invalid={Boolean(fieldErrors.maintenanceEnd)}
              aria-describedby={describedBy(fieldErrors.maintenanceEnd && "node-edit-maint-end-error", "node-edit-maint-hint")}
              value={draft.maintenanceEnd}
              onChange={(event) => updateDraft({ maintenanceEnd: event.target.value }, "maintenanceStart", "maintenanceEnd")}
            />
            <FieldError id="node-edit-maint-end-error" message={fieldErrors.maintenanceEnd} />
          </div>
        </div>
        <div className="flex flex-wrap items-start justify-between gap-x-3 gap-y-1">
          <p id="node-edit-maint-hint" className="min-w-0 flex-1 text-xs text-muted-foreground">
            {t('nodeEditor.maintenanceHint')} {t('nodeEditor.maintenanceTimezone', { zone: timeZoneLabel })}
          </p>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={!draft.maintenanceStart && !draft.maintenanceEnd}
            onClick={() => updateDraft({ maintenanceStart: "", maintenanceEnd: "" }, "maintenanceStart", "maintenanceEnd")}
          >
            {t('nodeEditor.clearMaintenance')}
          </Button>
        </div>
      </EditorSection>
    </FormDialog>
  );
}

export type { NodeEditorDraft };
