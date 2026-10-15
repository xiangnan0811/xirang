import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Activity, CheckCircle2, CircleDashed, Copy, Loader2, Stethoscope, TriangleAlert, XCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { toast } from "@/components/ui/toast-sonner";
import { getAuthIdentitySnapshot, getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";
import type { NodeDoctorCheckResult, NodeDoctorCheckStatus, NodeDoctorResult, NodeRecord } from "@/types/domain";

const statusTone: Record<NodeDoctorCheckStatus, "success" | "warning" | "destructive" | "neutral"> = {
  pass: "success",
  warn: "warning",
  fail: "destructive",
  skip: "neutral",
};

function summarizeChecks(checks: readonly NodeDoctorCheckResult[]) {
  return {
    total: checks.length,
    fail: checks.filter((check) => check.status === "fail").length,
    warn: checks.filter((check) => check.status === "warn").length,
    pass: checks.filter((check) => check.status === "pass").length,
    skip: checks.filter((check) => check.status === "skip").length,
  };
}

/**
 * 生成可复制的 Doctor 结果文本。只使用诊断响应中已展示的安全字段（生成时间、
 * 本地化检查名/状态、证据、建议），不接收 NodeRecord，也不序列化整个响应，
 * 因此不会带出节点名称、地址、路径、命令或原始 SSH 输出。
 */
export function formatNodeDoctorCopyText(result: NodeDoctorResult, t: TFunction): string {
  const header = [
    t("nodes.doctorCopyTitle"),
    t("nodes.doctorGeneratedAt", { time: result.generatedAt || "-" }),
    t("nodes.doctorSummary", summarizeChecks(result.checks)),
  ].join("\n");
  const checks = result.checks.map((check) => {
    const lines = [
      `${t(`nodes.doctorStatus_${check.status}`)} · ${t(`nodes.doctorCheck_${check.check}`, { defaultValue: check.check })}`,
      check.evidence || t("nodes.doctorNoEvidence"),
    ];
    if (check.suggestion) {
      lines.push(`${t("nodes.doctorSuggestionPrefix")}${check.suggestion}`);
    }
    return lines.join("\n");
  });
  return [header, ...checks].join("\n\n");
}

function DoctorStatusIcon({ status }: { status: NodeDoctorCheckStatus }) {
  if (status === "pass") {
    return <CheckCircle2 className="size-4 text-success" aria-hidden />;
  }
  if (status === "fail") {
    return <XCircle className="size-4 text-destructive" aria-hidden />;
  }
  if (status === "warn") {
    return <TriangleAlert className="size-4 text-warning" aria-hidden />;
  }
  return <CircleDashed className="size-4 text-muted-foreground" aria-hidden />;
}

function DoctorCheckRow({ check }: { check: NodeDoctorCheckResult }) {
  const { t } = useTranslation();

  return (
    <li className="rounded-lg border border-border bg-background/60 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <DoctorStatusIcon status={check.status} />
          <h3 className="text-sm font-medium text-foreground">
            {t(`nodes.doctorCheck_${check.check}`, { defaultValue: check.check })}
          </h3>
        </div>
        <Badge tone={statusTone[check.status]}>
          {t(`nodes.doctorStatus_${check.status}`)}
        </Badge>
      </div>
      <p className="mt-2 text-xs text-foreground/80">
        {check.evidence || t("nodes.doctorNoEvidence")}
      </p>
      {check.suggestion ? (
        <p className="mt-1 text-xs text-muted-foreground">
          {t("nodes.doctorSuggestionPrefix")}{check.suggestion}
        </p>
      ) : null}
    </li>
  );
}

export type NodeDoctorDialogProps = {
  open: boolean;
  node: NodeRecord | null;
  result: NodeDoctorResult | null;
  loading: boolean;
  error?: string | null;
  onOpenChange: (open: boolean) => void;
  onRun: (node: NodeRecord) => void;
  canOperateNodes: boolean;
  connectionNavigationAvailable: boolean;
  /** 返回 true 表示已安排关闭并回焦到连接测试按钮；不代表测试已执行或成功。 */
  onNavigateConnection: () => boolean;
  onCloseAutoFocus?: DialogCloseAutoFocus;
};

type CopyLifetime = {
  dialogGeneration: number;
  result: NodeDoctorResult;
  sessionGeneration: number;
  token: string | null;
  role: string | null;
};

export function NodeDoctorDialog({
  open,
  node,
  result,
  loading,
  error,
  onOpenChange,
  onRun,
  canOperateNodes,
  connectionNavigationAvailable,
  onNavigateConnection,
  onCloseAutoFocus,
}: NodeDoctorDialogProps) {
  const { t } = useTranslation();
  const summary = useMemo(() => summarizeChecks(result?.checks ?? []), [result]);

  // 本地对话框代次：打开/关闭/换节点都会使旧复制完成失去提示与状态写入权。
  const dialogGenerationRef = useRef(0);
  const latestRef = useRef({ open, result });
  const copyPendingRef = useRef<NodeDoctorResult | null>(null);
  const [copyingResult, setCopyingResult] = useState<NodeDoctorResult | null>(null);
  const nodeId = node?.id ?? null;
  useLayoutEffect(() => {
    dialogGenerationRef.current += 1;
    return () => {
      dialogGenerationRef.current += 1;
    };
  }, [open, nodeId]);
  useLayoutEffect(() => {
    latestRef.current = { open, result };
  }, [open, result]);

  const navigationResetKey = `${open}:${nodeId ?? ""}`;
  const [navigationState, setNavigationState] = useState({ key: navigationResetKey, unavailable: false });
  if (navigationState.key !== navigationResetKey) {
    setNavigationState({ key: navigationResetKey, unavailable: false });
  }

  const copying = copyingResult !== null && copyingResult === result;
  const copyAvailable = Boolean(
    open
      && canOperateNodes
      && node
      && result
      && result.nodeId === node.id
      && !loading
      && !error
      && !copying,
  );

  const ownsCopyLifetime = (captured: CopyLifetime): boolean => {
    const identity = getAuthIdentitySnapshot();
    return latestRef.current.open
      && latestRef.current.result === captured.result
      && dialogGenerationRef.current === captured.dialogGeneration
      && getAuthSessionGeneration() === captured.sessionGeneration
      && !isAuthTransitionActive()
      && identity.token === captured.token
      && identity.role === captured.role;
  };

  const handleCopy = async () => {
    if (!copyAvailable || !result || copyPendingRef.current === result || isAuthTransitionActive()) return;
    const identity = getAuthIdentitySnapshot();
    const captured: CopyLifetime = {
      dialogGeneration: dialogGenerationRef.current,
      result,
      sessionGeneration: getAuthSessionGeneration(),
      token: identity.token,
      role: identity.role,
    };
    copyPendingRef.current = result;
    setCopyingResult(result);
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error("clipboard unavailable");
      }
      await navigator.clipboard.writeText(formatNodeDoctorCopyText(result, t));
      if (ownsCopyLifetime(captured)) toast.success(t("common.copiedToClipboard"));
    } catch {
      if (ownsCopyLifetime(captured)) toast.error(t("common.copyFailed"));
    } finally {
      if (ownsCopyLifetime(captured)) {
        copyPendingRef.current = null;
        setCopyingResult(null);
      }
    }
  };

  const handleNavigateConnection = () => {
    if (!connectionNavigationAvailable) return;
    if (!onNavigateConnection()) {
      setNavigationState({ key: navigationResetKey, unavailable: true });
    }
  };

  const navigationUnavailable = !connectionNavigationAvailable || navigationState.unavailable;
  const navigationHintId = "node-doctor-connection-unavailable";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg" onCloseAutoFocus={onCloseAutoFocus}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Stethoscope className="size-4" aria-hidden />
            {t("nodes.doctorTitle", { name: node?.name ?? "" })}
          </DialogTitle>
          <DialogDescription>
            {t("nodes.doctorDesc")}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <div className="space-y-3 px-6 py-2">
          <div className="rounded-lg border border-border bg-secondary/40 p-3 text-xs text-muted-foreground">
            {result ? (
              <div className="flex flex-wrap gap-2">
                <span>{t("nodes.doctorGeneratedAt", { time: result.generatedAt || "-" })}</span>
                <span>{t("nodes.doctorSummary", summary)}</span>
              </div>
            ) : (
              <span>{t("nodes.doctorEmpty")}</span>
            )}
          </div>

          {error ? (
            <div role="alert" className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
              {error}
            </div>
          ) : null}

          {loading ? (
            <div className="flex items-center gap-2 rounded-lg border border-border p-4 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" aria-hidden />
              {t("nodes.doctorRunning")}
            </div>
          ) : null}

          {result && !loading ? (
            <ul className="max-h-[50vh] space-y-2 overflow-y-auto pr-1 thin-scrollbar" aria-label={t("nodes.doctorResultsAriaLabel")}>
              {result.checks.map((check) => (
                <DoctorCheckRow key={check.check} check={check} />
              ))}
            </ul>
          ) : null}

          {canOperateNodes && navigationUnavailable ? (
            <p id={navigationHintId} className="text-xs text-muted-foreground">
              {t("nodes.doctorConnectionUnavailable")}
            </p>
          ) : null}
        </div>

        <DialogFooter className="flex-wrap">
          {canOperateNodes ? (
            <Button
              type="button"
              variant="outline"
              onClick={handleNavigateConnection}
              disabled={!connectionNavigationAvailable}
              aria-describedby={navigationUnavailable ? navigationHintId : undefined}
            >
              <Activity className="size-4" aria-hidden />
              {t("nodes.doctorGoToConnectionTest")}
            </Button>
          ) : null}
          <Button
            type="button"
            variant="outline"
            onClick={() => void handleCopy()}
            disabled={!copyAvailable}
          >
            {copying ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Copy className="size-4" aria-hidden />}
            {t("nodes.doctorCopy")}
          </Button>
          <Button
            type="button"
            onClick={() => node && onRun(node)}
            disabled={!node || loading}
          >
            {loading ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Stethoscope className="size-4" aria-hidden />}
            {result ? t("nodes.doctorRerun") : t("nodes.doctorRun")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
