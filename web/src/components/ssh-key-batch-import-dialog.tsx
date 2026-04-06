import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, CheckCircle2, FileUp, Upload, XCircle } from "lucide-react";
import { toast } from "@/components/ui/toast";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { createSSHKeysApi } from "@/lib/api/ssh-keys-api";
import { parseSSHKeyType, type NewSSHKeyInput } from "@/types/domain";

// ── 类型定义 ──

type ImportPhase = "idle" | "preview" | "importing" | "done";

type ValidationStatus = "valid" | "name_exists" | "format_error";

interface ParsedEntry {
  /** 原始 JSON 中的序号（从 1 开始） */
  index: number;
  name: string;
  username: string;
  keyType: string;
  privateKey: string;
  status: ValidationStatus;
  error?: string;
}

interface SSHKeyBatchImportDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  existingKeyNames: string[];
  token: string;
  onImportComplete: () => void;
}

// ── 校验逻辑 ──

function validateEntries(
  raw: unknown,
  existingNames: Set<string>,
): ParsedEntry[] {
  if (!Array.isArray(raw)) return [];

  return raw.map((item, idx): ParsedEntry => {
    const index = idx + 1;

    // 基本类型校验
    if (typeof item !== "object" || item === null) {
      return { index, name: "", username: "", keyType: "", privateKey: "", status: "format_error" };
    }

    const obj = item as Record<string, unknown>;
    const name = typeof obj.name === "string" ? obj.name.trim() : "";
    const username = typeof obj.username === "string" ? obj.username.trim() : "";
    const keyType = typeof obj.keyType === "string" ? obj.keyType : "auto";
    const privateKey = typeof obj.privateKey === "string" ? obj.privateKey.trim() : "";

    // 必填字段校验
    if (!name || !username || !privateKey) {
      return { index, name, username, keyType, privateKey, status: "format_error" };
    }

    // 重名检测
    if (existingNames.has(name)) {
      return { index, name, username, keyType, privateKey, status: "name_exists" };
    }

    return { index, name, username, keyType, privateKey, status: "valid" };
  });
}

// ── 组件 ──

export function SSHKeyBatchImportDialog({
  open,
  onOpenChange,
  existingKeyNames,
  token,
  onImportComplete,
}: SSHKeyBatchImportDialogProps) {
  const { t } = useTranslation();
  const fileInputRef = useRef<HTMLInputElement>(null);

  const [phase, setPhase] = useState<ImportPhase>("idle");
  const [entries, setEntries] = useState<ParsedEntry[]>([]);
  const [dragOver, setDragOver] = useState(false);
  const [parseError, setParseError] = useState("");

  // 打开时重置所有状态
  useEffect(() => {
    if (open) {
      setPhase("idle");
      setEntries([]);
      setDragOver(false);
      setParseError("");
    }
  }, [open]);

  const existingNamesSet = new Set(existingKeyNames);
  const validEntries = entries.filter((e) => e.status === "valid");

  // ── 文件处理 ──

  const processFile = useCallback(
    (file: File) => {
      setParseError("");

      if (!file.name.endsWith(".json")) {
        setParseError(t("sshKeys.jsonFormatOnly"));
        return;
      }

      if (file.size > 1024 * 1024) {
        setParseError(t("sshKeys.fileTooLarge"));
        return;
      }

      const reader = new FileReader();
      reader.onload = (e) => {
        const text = e.target?.result;
        if (typeof text !== "string") return;

        try {
          const parsed: unknown = JSON.parse(text);
          const result = validateEntries(parsed, existingNamesSet);
          if (result.length === 0) {
            setParseError(t("sshKeys.formatError"));
            return;
          }
          setEntries(result);
          setPhase("preview");
        } catch {
          setParseError(t("sshKeys.formatError"));
        }
      };
      reader.onerror = () => {
        setParseError(t("sshKeys.fileReadFailed"));
      };
      reader.readAsText(file);
    },
    // existingNamesSet 每次渲染都是新引用，用 existingKeyNames 作为依赖
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [t, existingKeyNames],
  );

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) processFile(file);
    e.target.value = "";
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDragOver(false);
    const file = e.dataTransfer.files[0];
    if (file) processFile(file);
  };

  const handleDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    setDragOver(true);
  };

  const handleDragLeave = (e: React.DragEvent) => {
    e.preventDefault();
    setDragOver(false);
  };

  // ── 导入 ──

  const handleImport = async () => {
    if (validEntries.length === 0) return;
    setPhase("importing");

    const keys: NewSSHKeyInput[] = validEntries.map((e) => ({
      name: e.name,
      username: e.username,
      keyType: parseSSHKeyType(e.keyType),
      privateKey: e.privateKey,
    }));

    try {
      const results = await createSSHKeysApi().batchCreate(token, keys);
      const created = results.filter((r) => r.status === "created").length;
      toast.success(t("sshKeys.importSuccess", { count: created }));
      setPhase("done");
      onImportComplete();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("common.operationFailed"));
      // 回退到预览状态以便用户重试
      setPhase("preview");
    }
  };

  // ── 状态图标 ──

  const statusBadge = (status: ValidationStatus) => {
    switch (status) {
      case "valid":
        return (
          <span className="inline-flex items-center gap-1 text-xs text-success">
            <CheckCircle2 className="size-3.5" />
            {t("sshKeys.validKey")}
          </span>
        );
      case "name_exists":
        return (
          <span className="inline-flex items-center gap-1 text-xs text-warning">
            <AlertCircle className="size-3.5" />
            {t("sshKeys.nameExists")}
          </span>
        );
      case "format_error":
        return (
          <span className="inline-flex items-center gap-1 text-xs text-destructive">
            <XCircle className="size-3.5" />
            {t("sshKeys.formatError")}
          </span>
        );
    }
  };

  // ── 渲染 ──

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>{t("sshKeys.batchImportTitle")}</DialogTitle>
          <DialogDescription>{t("sshKeys.batchImportDesc")}</DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-4">
          {/* ── idle：上传区域 ── */}
          {phase === "idle" && (
            <>
              {/* 拖拽上传区域 */}
              <button
                type="button"
                className={`flex w-full flex-col items-center gap-3 rounded-xl border-2 border-dashed px-6 py-10 transition-colors ${
                  dragOver
                    ? "border-primary bg-primary/5"
                    : "border-border hover:border-primary/40 hover:bg-muted/30"
                }`}
                onClick={() => fileInputRef.current?.click()}
                onDrop={handleDrop}
                onDragOver={handleDragOver}
                onDragLeave={handleDragLeave}
              >
                <div className="flex size-12 items-center justify-center rounded-full bg-muted">
                  <Upload className="size-5 text-muted-foreground" />
                </div>
                <div className="text-center">
                  <p className="text-sm font-medium">{t("sshKeys.dropOrUpload")}</p>
                  <p className="mt-1 text-xs text-muted-foreground">{t("sshKeys.jsonFormatOnly")}</p>
                </div>
              </button>
              <input
                ref={fileInputRef}
                type="file"
                className="hidden"
                accept=".json"
                onChange={handleFileChange}
              />

              {/* 解析错误提示 */}
              {parseError && (
                <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  {parseError}
                </div>
              )}

              {/* JSON 格式示例 */}
              <div>
                <p className="mb-1.5 text-xs font-medium text-muted-foreground">
                  {t("sshKeys.jsonFormatHint")}
                </p>
                <pre className="overflow-x-auto rounded-lg bg-muted/50 p-3 text-xs leading-relaxed">
{`[
  {
    "name": "prod-deploy",
    "username": "deploy",
    "keyType": "auto",
    "privateKey": "-----BEGIN OPENSSH PRIVATE KEY-----\\n..."
  }
]`}
                </pre>
              </div>
            </>
          )}

          {/* ── preview / importing：预览列表 ── */}
          {(phase === "preview" || phase === "importing") && (
            <>
              <p className="text-sm font-medium">
                {t("sshKeys.previewTitle", { count: entries.length })}
              </p>
              <div className="max-h-72 overflow-y-auto rounded-lg border border-border">
                <table className="w-full text-sm">
                  <thead className="sticky top-0 bg-muted/80 backdrop-blur-sm">
                    <tr className="border-b border-border text-left">
                      <th className="px-3 py-2 font-medium">#</th>
                      <th className="px-3 py-2 font-medium">{t("sshKeys.keyName")}</th>
                      <th className="px-3 py-2 font-medium">{t("sshKeys.defaultUsername")}</th>
                      <th className="px-3 py-2 font-medium text-right">{t("common.status")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {entries.map((entry) => (
                      <tr
                        key={entry.index}
                        className="border-b border-border/50 last:border-0"
                      >
                        <td className="px-3 py-2 text-muted-foreground">{entry.index}</td>
                        <td className="px-3 py-2 font-mono text-xs">
                          {entry.name || "—"}
                        </td>
                        <td className="px-3 py-2 text-muted-foreground">
                          {entry.username || "—"}
                        </td>
                        <td className="px-3 py-2 text-right">
                          {statusBadge(entry.status)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {/* 重新选择文件 */}
              {phase === "preview" && (
                <button
                  type="button"
                  className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
                  onClick={() => {
                    setPhase("idle");
                    setEntries([]);
                  }}
                >
                  <FileUp className="size-3.5" />
                  {t("sshKeys.dropOrUpload")}
                </button>
              )}
            </>
          )}
        </DialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          {(phase === "preview" || phase === "importing") && (
            <Button
              onClick={() => void handleImport()}
              disabled={phase === "importing" || validEntries.length === 0}
              loading={phase === "importing"}
            >
              {phase === "importing"
                ? t("sshKeys.importing")
                : t("sshKeys.importValidKeys", { count: validEntries.length })}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
