import { useState, useEffect, useCallback } from "react";
import { useTranslation } from "react-i18next";
import { CheckCircle2, XCircle, AlertTriangle, SkipForward, Loader2, ArrowRightLeft } from "lucide-react";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogCloseButton } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { InlineAlert } from "@/components/ui/inline-alert";
import { toast } from "@/components/ui/toast";
import { apiClient } from "@/lib/api/client";
import type { PreflightCheckStatus, MigratePreflightResult, MigrateNodeResult } from "@/lib/api/nodes-api";
import { getErrorMessage } from "@/lib/utils";

interface NodeRecord {
  id: number;
  name: string;
  host: string;
  status: string;
  archived?: boolean;
  disk_used_gb?: number;
  disk_total_gb?: number;
}

interface NodeMigrateWizardProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sourceNode: NodeRecord | null;
  nodes: NodeRecord[];
  token: string;
  onSuccess: () => void;
}

type Step = 1 | 2 | 3 | 4;

const statusIcons: Record<PreflightCheckStatus, React.ReactNode> = {
  pass: <CheckCircle2 className="size-4 text-success" />,
  fail: <XCircle className="size-4 text-destructive" />,
  warn: <AlertTriangle className="size-4 text-warning" />,
  skip: <SkipForward className="size-4 text-muted-foreground" />,
};

const dataStatusIcons: Record<string, React.ReactNode> = {
  copied: <CheckCircle2 className="size-3.5 text-success" />,
  skipped: <SkipForward className="size-3.5 text-muted-foreground" />,
  error: <XCircle className="size-3.5 text-destructive" />,
};

export function NodeMigrateWizard({ open, onOpenChange, sourceNode, nodes, token, onSuccess }: NodeMigrateWizardProps) {
  const { t } = useTranslation();

  const [step, setStep] = useState<Step>(1);
  const [targetNodeId, setTargetNodeId] = useState<number | null>(null);
  const [preflight, setPreflight] = useState<MigratePreflightResult | null>(null);
  const [preflightLoading, setPreflightLoading] = useState(false);
  const [preflightError, setPreflightError] = useState<string | null>(null);
  const [archiveSource, setArchiveSource] = useState(false);
  const [pausePolicies, setPausePolicies] = useState(false);
  const [migrateData, setMigrateData] = useState(false);
  const [migrating, setMigrating] = useState(false);
  const [migrateResult, setMigrateResult] = useState<MigrateNodeResult | null>(null);
  const [migrateError, setMigrateError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setStep(1);
      setTargetNodeId(null);
      setPreflight(null);
      setPreflightLoading(false);
      setPreflightError(null);
      setArchiveSource(false);
      setPausePolicies(false);
      setMigrateData(false);
      setMigrating(false);
      setMigrateResult(null);
      setMigrateError(null);
    }
  }, [open, sourceNode?.id]);

  const availableTargets = nodes.filter((n) => n.id !== sourceNode?.id && !n.archived);

  const runPreflight = useCallback(async () => {
    if (!sourceNode || !targetNodeId) return;
    setPreflightLoading(true);
    setPreflightError(null);
    setPreflight(null);
    try {
      const result = await apiClient.migrateNodePreflight(token, sourceNode.id, targetNodeId);
      setPreflight(result);
    } catch (err) {
      setPreflightError(getErrorMessage(err));
    } finally {
      setPreflightLoading(false);
    }
  }, [token, sourceNode, targetNodeId]);

  const runMigrate = useCallback(async () => {
    if (!sourceNode || !targetNodeId) return;
    setMigrating(true);
    setMigrateError(null);
    try {
      const result = await apiClient.migrateNode(token, sourceNode.id, targetNodeId, {
        archiveSource,
        pausePolicies,
        migrateData,
      });
      setMigrateResult(result);
      toast.success(t("nodes.migrateSuccess", { policies: result.migratedPolicies, tasks: result.migratedTasks }));
    } catch (err) {
      setMigrateError(getErrorMessage(err));
    } finally {
      setMigrating(false);
    }
  }, [token, sourceNode, targetNodeId, archiveSource, pausePolicies, migrateData, t]);

  const handleNext = () => {
    if (step === 1 && targetNodeId) {
      setStep(2);
      void runPreflight();
    } else if (step === 2 && preflight?.canProceed) {
      setStep(3);
    } else if (step === 3) {
      setStep(4);
      void runMigrate();
    }
  };

  const handleBack = () => {
    if (step === 2) setStep(1);
    else if (step === 3) setStep(2);
  };

  const handleDone = () => {
    onOpenChange(false);
    if (migrateResult) onSuccess();
  };

  if (!sourceNode) return null;

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o && !migrating) onOpenChange(false); }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ArrowRightLeft className="size-5" />
            {t("nodes.migrateWizardTitle", { name: sourceNode.name })}
          </DialogTitle>
          <DialogDescription>{t("nodes.migrateWizardDesc")}</DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        {/* Step indicator */}
        <div className="flex items-center justify-center gap-1 px-6 pb-2 text-xs text-muted-foreground">
          {([1, 2, 3, 4] as Step[]).map((s) => (
            <div key={s} className="flex items-center gap-1">
              <span
                role="presentation"
                aria-label={`${t("nodes.migrateStep")} ${s}`}
                className={`flex size-6 items-center justify-center rounded-full text-xs font-medium ${
                  s === step ? "bg-primary text-primary-foreground" : s < step ? "bg-primary/20 text-primary" : "bg-muted text-muted-foreground"
                }`}
              >
                {s}
              </span>
              {s < 4 && <span className="mx-0.5 text-muted-foreground/40">—</span>}
            </div>
          ))}
        </div>

        <div className="space-y-4 px-6 pb-6">
          {/* Step 1: Select target */}
          {step === 1 && (
            <>
              <p className="text-sm text-muted-foreground">{t("nodes.migrateSelectTargetDesc", { name: sourceNode.name })}</p>
              <div>
                <label htmlFor="migrate-target" className="mb-1 block text-sm font-medium">
                  {t("nodes.migrateTargetLabel")}
                </label>
                <Select
                  id="migrate-target"
                  containerClassName="w-full"
                  value={targetNodeId ? String(targetNodeId) : ""}
                  onChange={(e) => setTargetNodeId(e.target.value ? Number(e.target.value) : null)}
                >
                  <option value="">{t("nodes.migrateTargetPlaceholder")}</option>
                  {availableTargets.map((n) => (
                    <option key={n.id} value={String(n.id)}>
                      {n.name} ({n.host})
                    </option>
                  ))}
                </Select>
              </div>
              <div className="flex justify-end pt-2">
                <Button disabled={!targetNodeId} onClick={handleNext}>{t("nodes.migrateStepNext")}</Button>
              </div>
            </>
          )}

          {/* Step 2: Preflight checks */}
          {step === 2 && (
            <>
              {preflightLoading && (
                <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
                  <Loader2 className="size-4 animate-spin" />
                  {t("nodes.migrateRunPreflight")}
                </div>
              )}

              {preflightError && (
                <InlineAlert tone="critical">{preflightError}</InlineAlert>
              )}

              {preflight && (
                <div className="space-y-2">
                  <p className="text-sm font-medium">{t("nodes.migratePreflightTitle")}</p>
                  <div className="space-y-1.5">
                    {preflight.checks.map((check, i) => (
                      <div key={i} className="flex items-start gap-2 rounded-md border px-3 py-2 text-sm">
                        <span className="mt-0.5 shrink-0">{statusIcons[check.status]}</span>
                        <span className="text-muted-foreground">{check.message}</span>
                      </div>
                    ))}
                  </div>
                  {!preflight.canProceed && (
                    <p className="text-sm text-destructive">{t("nodes.migrateCannotProceed")}</p>
                  )}
                </div>
              )}

              <div className="flex justify-between pt-2">
                <Button variant="outline" onClick={handleBack}>{t("nodes.migrateStepPrev")}</Button>
                <Button disabled={!preflight?.canProceed} onClick={handleNext}>{t("nodes.migrateStepNext")}</Button>
              </div>
            </>
          )}

          {/* Step 3: Confirm */}
          {step === 3 && preflight && (
            <>
              <div className="space-y-2">
                <p className="text-sm">
                  {t("nodes.migrateSummary", { policies: preflight.policies.length, tasks: preflight.taskCount })}
                </p>
                <div className="text-sm text-muted-foreground">
                  <span className="font-medium text-foreground">{preflight.sourceNode.name}</span>
                  {" → "}
                  <span className="font-medium text-foreground">{preflight.targetNode.name}</span>
                </div>

                {preflight.policies.length > 0 && (
                  <div className="max-h-32 overflow-y-auto rounded border p-2 text-xs">
                    {preflight.policies.map((p) => (
                      <div key={p.id} className="flex items-center justify-between py-0.5">
                        <span>{p.name}</span>
                        <span className="text-muted-foreground">{p.executorType}</span>
                      </div>
                    ))}
                  </div>
                )}
              </div>

              <div className="space-y-2">
                <label htmlFor="migrate-opt-data" className="flex items-center gap-2 text-sm">
                  <input id="migrate-opt-data" type="checkbox" checked={migrateData} onChange={(e) => setMigrateData(e.target.checked)} className="rounded" />
                  {t("nodes.migrateOptData")}
                  {preflight.dataMigratable && preflight.dataSizeMb > 0 && (
                    <span className="text-xs text-muted-foreground">
                      (~{preflight.dataSizeMb >= 1024 ? `${(preflight.dataSizeMb / 1024).toFixed(1)}GB` : `${preflight.dataSizeMb}MB`})
                    </span>
                  )}
                </label>
                {migrateData && (
                  <p className="ml-6 text-xs text-muted-foreground">{t("nodes.migrateOptDataDesc")}</p>
                )}
                {!preflight.dataMigratable && migrateData && (
                  <p className="ml-6 text-xs text-warning">{t("nodes.migrateNoLocalData")}</p>
                )}
                <label htmlFor="migrate-opt-archive" className="flex items-center gap-2 text-sm">
                  <input id="migrate-opt-archive" type="checkbox" checked={archiveSource} onChange={(e) => setArchiveSource(e.target.checked)} className="rounded" />
                  {t("nodes.migrateOptArchive")}
                </label>
                <label htmlFor="migrate-opt-pause" className="flex items-center gap-2 text-sm">
                  <input id="migrate-opt-pause" type="checkbox" checked={pausePolicies} onChange={(e) => setPausePolicies(e.target.checked)} className="rounded" />
                  {t("nodes.migrateOptPause")}
                </label>
              </div>

              {!migrateData && (
                <InlineAlert tone="warning">{t("nodes.migrateConfigOnlyWarning")}</InlineAlert>
              )}
              {migrateData && (
                <InlineAlert tone="info">{t("nodes.migrateDataInfo")}</InlineAlert>
              )}

              <div className="flex justify-between pt-2">
                <Button variant="outline" onClick={handleBack}>{t("nodes.migrateStepPrev")}</Button>
                <Button onClick={handleNext}>{t("nodes.migrateStepConfirm")}</Button>
              </div>
            </>
          )}

          {/* Step 4: Execute */}
          {step === 4 && (
            <>
              {migrating && (
                <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
                  <Loader2 className="size-4 animate-spin" />
                  {migrateData ? t("nodes.migrateExecutingWithData") : t("nodes.migrateExecuting")}
                </div>
              )}

              {migrateResult && (
                <div className="space-y-3">
                  <InlineAlert tone="success" title={t("nodes.migrateResultTitle")}>
                    {t("nodes.migrateSuccess", { policies: migrateResult.migratedPolicies, tasks: migrateResult.migratedTasks })}
                  </InlineAlert>

                  {migrateResult.dataMigration && migrateResult.dataMigration.length > 0 && (
                    <div className="space-y-1.5">
                      <p className="text-sm font-medium">{t("nodes.migrateDataResultTitle")}</p>
                      <div className="max-h-32 overflow-y-auto rounded border p-2 text-xs space-y-1">
                        {migrateResult.dataMigration.map((item, i) => (
                          <div key={i} className="flex items-start gap-1.5">
                            <span className="mt-0.5 shrink-0">{dataStatusIcons[item.status]}</span>
                            <span>
                              <span className="font-medium">{item.policyName}</span>
                              <span className="text-muted-foreground"> — {item.message}</span>
                            </span>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                </div>
              )}

              {migrateError && (
                <InlineAlert tone="critical">{migrateError}</InlineAlert>
              )}

              <div className="flex justify-end gap-2 pt-2">
                {migrateError && (
                  <Button variant="outline" onClick={() => void runMigrate()}>{t("nodes.migrateStepRetry")}</Button>
                )}
                <Button onClick={handleDone} disabled={migrating}>
                  {migrateResult ? t("nodes.migrateStepDone") : migrating ? t("nodes.migrateExecuting") : t("common.cancel")}
                </Button>
              </div>
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
