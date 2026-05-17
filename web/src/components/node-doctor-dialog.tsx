import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { CheckCircle2, CircleDashed, Loader2, Stethoscope, TriangleAlert, XCircle } from "lucide-react";
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
import type { NodeDoctorCheckResult, NodeDoctorCheckStatus, NodeDoctorResult, NodeRecord } from "@/types/domain";

const statusTone: Record<NodeDoctorCheckStatus, "success" | "warning" | "destructive" | "neutral"> = {
  pass: "success",
  warn: "warning",
  fail: "destructive",
  skip: "neutral",
};

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
};

export function NodeDoctorDialog({
  open,
  node,
  result,
  loading,
  error,
  onOpenChange,
  onRun,
}: NodeDoctorDialogProps) {
  const { t } = useTranslation();
  const summary = useMemo(() => {
    const checks = result?.checks ?? [];
    return {
      total: checks.length,
      fail: checks.filter((check) => check.status === "fail").length,
      warn: checks.filter((check) => check.status === "warn").length,
      pass: checks.filter((check) => check.status === "pass").length,
      skip: checks.filter((check) => check.status === "skip").length,
    };
  }, [result]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg">
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
        </div>

        <DialogFooter>
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
