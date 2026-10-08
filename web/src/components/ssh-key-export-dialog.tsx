import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import { sensitiveStepUpBlock } from "@/lib/sensitive-step-up";
import { getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";
import { Download, FileText, FileJson, FileSpreadsheet } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogBody,
  DialogFooter,
  DialogCloseButton,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast-sonner";
import { useStepUpAction } from "@/hooks/use-step-up-action";
import { createSSHKeysApi, fetchSSHKeyExportFile } from "@/lib/api/ssh-keys-api";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { getErrorMessage } from "@/lib/utils";
import type { SSHKeyRecord } from "@/types/domain";

type ExportFormat = "authorized_keys" | "json" | "csv";
type ExportScope = "all" | "in_use" | "selected";

interface SSHKeyExportDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sshKeys: SSHKeyRecord[];
  selectedKeyIds: string[];
  stats: { total: number; inUse: number };
  token: string;
}

/** 生成前端预览文本 */
function generatePreview(
  keys: SSHKeyRecord[],
  format: ExportFormat,
  maxLines: number,
): string {
  if (keys.length === 0) return "";

  const preview = keys.slice(0, maxLines);

  switch (format) {
    case "authorized_keys":
      return preview
        .map((k) => k.publicKey || `ssh-ed25519 AAAA... ${k.name}`)
        .join("\n");

    case "json":
      return JSON.stringify(
        preview.map((k) => ({
          name: k.name,
          fingerprint: k.fingerprint,
          public_key: k.publicKey || "",
          created_at: k.createdAt,
        })),
        null,
        2,
      );

    case "csv": {
      const header = "name,fingerprint,public_key,created_at";
      const rows = preview.map(
        (k) =>
          `"${k.name}","${k.fingerprint}","${k.publicKey || ""}","${k.createdAt}"`,
      );
      return [header, ...rows].join("\n");
    }
  }
}

const FORMAT_OPTIONS: {
  value: ExportFormat;
  icon: typeof FileText;
}[] = [
  { value: "authorized_keys", icon: FileText },
  { value: "json", icon: FileJson },
  { value: "csv", icon: FileSpreadsheet },
];

const DOWNLOAD_FILENAMES: Record<ExportFormat, string> = {
  authorized_keys: "authorized_keys",
  json: "ssh-keys.json",
  csv: "ssh-keys.csv",
};

class StaleExportOperation extends Error {
  constructor() {
    super("stale-export");
    this.name = "StaleExportOperation";
  }
}

function useCommittedDialogScope(
  role: AuthRole | null,
  token: string,
  open: boolean,
  authGeneration: number,
  authTransitioning: boolean,
) {
  const mountedRef = useRef(false);
  const openRef = useRef(open);
  const generationRef = useRef(0);

  useLayoutEffect(() => {
    openRef.current = open;
    generationRef.current += 1;
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      openRef.current = false;
      generationRef.current += 1;
    };
  }, [authGeneration, authTransitioning, open, role, token]);

  const isCurrent = useCallback((generation: number, sessionGeneration: number) => (
    mountedRef.current
    && openRef.current
    && generationRef.current === generation
    && getAuthSessionGeneration() === sessionGeneration
    && !isAuthTransitionActive()
  ), []);

  return { generationRef, isCurrent };
}

export function SSHKeyExportDialog(props: SSHKeyExportDialogProps) {
  const { role, authTransitioning = false } = useAuth();
  const authGeneration = getAuthSessionGeneration();
  const sessionKey = [
    props.open ? "1" : "0",
    props.token,
    role ?? "",
    String(authGeneration),
    authTransitioning ? "1" : "0",
  ].join("|");
  return <SSHKeyExportSession key={sessionKey} {...props} />;
}

function SSHKeyExportSession({
  open,
  onOpenChange,
  sshKeys,
  selectedKeyIds,
  stats,
  token,
}: SSHKeyExportDialogProps) {
  const { t } = useTranslation();
  const { role, totpEnabled, authTransitioning = false } = useAuth();
  const authGeneration = getAuthSessionGeneration();
  const { generationRef, isCurrent } = useCommittedDialogScope(
    role,
    token,
    open,
    authGeneration,
    authTransitioning,
  );
  const [downloading, setDownloading] = useState(false);
  const [format, setFormat] = useState<ExportFormat>("authorized_keys");
  const [scope, setScope] = useState<ExportScope>("all");
  const withStepUp = useStepUpAction(STEP_UP_ACTIONS.sshKeyExport);

  // 根据 scope 筛选出预览使用的密钥列表
  const filteredKeys = useMemo(() => {
    switch (scope) {
      case "all":
        return sshKeys;
      case "in_use":
        // 在用密钥：存在关联节点的密钥（通过 stats 无法精确判断，用 lastUsedAt 近似）
        // 但更准确的做法是看 selectedKeyIds 的逻辑不需要，这里返回全部让后端处理
        return sshKeys;
      case "selected":
        return sshKeys.filter((k) => selectedKeyIds.includes(k.id));
      default:
        return sshKeys;
    }
  }, [sshKeys, scope, selectedKeyIds]);

  const preview = useMemo(
    () => generatePreview(filteredKeys, format, 3),
    [filteredKeys, format],
  );

  // 获取各 scope 的数量
  const scopeCount = useMemo(
    () => ({
      all: stats.total,
      in_use: stats.inUse,
      selected: selectedKeyIds.length,
    }),
    [stats, selectedKeyIds],
  );

  const handleDownload = async () => {
    const generation = generationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    if (!isCurrent(generation, sessionGeneration)) return;
    if (sensitiveStepUpBlock({ token, totpEnabled }) !== "ready") return;
    const exportStillCurrent = () => isCurrent(generation, sessionGeneration);
    setDownloading(true);
    try {
      const apiClient = createSSHKeysApi();
      // scope 为 selected 时，API 使用 scope=all + 传入 ids
      const apiScope = scope === "selected" ? "all" : scope;
      const ids = scope === "selected" ? selectedKeyIds : undefined;
      const url = apiClient.getExportUrl(format, apiScope, ids);

      const response = await withStepUp(async (proof) => {
        if (!exportStillCurrent()) {
          throw new StaleExportOperation();
        }
        try {
          return await fetchSSHKeyExportFile(url, token, proof);
        } catch (error) {
          if (!exportStillCurrent()) {
            throw new StaleExportOperation();
          }
          throw error;
        }
      });

      if (!exportStillCurrent()) return;
      const blob = await response.blob();
      if (!exportStillCurrent()) return;
      const objectUrl = URL.createObjectURL(blob);
      if (!exportStillCurrent()) {
        URL.revokeObjectURL(objectUrl);
        return;
      }
      const anchor = document.createElement("a");
      anchor.href = objectUrl;
      anchor.download = DOWNLOAD_FILENAMES[format];
      anchor.click();
      URL.revokeObjectURL(objectUrl);
    } catch (err) {
      if (!exportStillCurrent() || err instanceof StaleExportOperation) return;
      if (sensitiveStepUpBlock({ token, totpEnabled }) !== "ready") return;
      toast.error(getErrorMessage(err, t("sshKeys.exportFailed")));
    } finally {
      if (exportStillCurrent()) {
        setDownloading(false);
      }
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>{t("sshKeys.exportTitle")}</DialogTitle>
          <DialogDescription>{t("sshKeys.exportDesc")}</DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-5">
          {totpEnabled === false ? <StepUpPrerequisiteNotice /> : null}
          {/* 格式选择 */}
          <div className="space-y-2">
            <label className="text-sm font-medium">
              {t("sshKeys.exportFormat")}
            </label>
            <div className="grid grid-cols-3 gap-2">
              {FORMAT_OPTIONS.map(({ value, icon: Icon }) => (
                <button
                  key={value}
                  type="button"
                  className={`flex flex-col items-center gap-1.5 rounded-lg border p-3 text-sm transition-colors ${
                    format === value
                      ? "border-primary bg-primary/5 text-primary"
                      : "border-border/60 text-muted-foreground hover:border-primary/30 hover:bg-accent/40"
                  }`}
                  onClick={() => setFormat(value)}
                  aria-pressed={format === value}
                >
                  <Icon className="size-5" />
                  <span className="font-medium">
                    {t(`sshKeys.format${value === "authorized_keys" ? "AuthorizedKeys" : value.toUpperCase()}`)}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {t(`sshKeys.format${value === "authorized_keys" ? "AuthorizedKeys" : value.toUpperCase()}Desc`)}
                  </span>
                </button>
              ))}
            </div>
            <p className="text-xs text-muted-foreground">
              {format === "authorized_keys"
                ? t("sshKeys.exportAuthorizedKeysFieldNote")
                : t("sshKeys.exportDigestFieldNote")}
            </p>
          </div>

          {/* 范围选择 */}
          <div className="space-y-2">
            <label className="text-sm font-medium">
              {t("sshKeys.exportScope")}
            </label>
            <div className="space-y-1.5">
              {(
                [
                  { value: "all", countKey: "all" },
                  { value: "in_use", countKey: "in_use" },
                  { value: "selected", countKey: "selected" },
                ] as const
              ).map(({ value, countKey }) => {
                const disabled =
                  value === "selected" && selectedKeyIds.length === 0;
                return (
                  <label
                    key={value}
                    className={`flex cursor-pointer items-center gap-2.5 rounded-md px-3 py-2 text-sm transition-colors ${
                      scope === value
                        ? "bg-primary/5 text-foreground"
                        : "text-muted-foreground hover:bg-accent/40"
                    } ${disabled ? "cursor-not-allowed opacity-50" : ""}`}
                  >
                    <input
                      type="radio"
                      name="export-scope"
                      value={value}
                      checked={scope === value}
                      disabled={disabled}
                      onChange={() => setScope(value)}
                      className="accent-primary"
                    />
                    <span>
                      {t(
                        `sshKeys.scope${
                          countKey === "all"
                            ? "All"
                            : countKey === "in_use"
                              ? "InUse"
                              : "Selected"
                        }`,
                        { count: scopeCount[countKey] },
                      )}
                    </span>
                  </label>
                );
              })}
            </div>
          </div>

          {/* 预览区域 */}
          {preview ? (
            <div className="space-y-2">
              <label className="text-sm font-medium text-muted-foreground">
                {t("sshKeys.exportPreview")}
              </label>
              <pre className="max-h-40 overflow-auto rounded-md border border-border/60 bg-muted/30 p-3 text-xs leading-relaxed thin-scrollbar">
                {preview}
              </pre>
            </div>
          ) : null}
        </DialogBody>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            {t("common.cancel")}
          </Button>
          <Button
            onClick={handleDownload}
            loading={downloading}
            disabled={downloading}
          >
            <Download className="mr-1.5 size-4" />
            {t("sshKeys.downloadFile")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
