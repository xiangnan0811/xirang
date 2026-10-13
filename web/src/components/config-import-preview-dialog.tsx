import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import type {
  ConfigImportConflict,
  ConfigImportEntity,
  ConfigImportPreview,
} from "@/types/domain";

const PREVIEW_ENTITIES: ConfigImportEntity[] = ["nodes", "ssh_keys", "policies", "tasks", "system_settings"];

const PREVIEW_ENTITY_LABEL: Record<ConfigImportEntity, "nodes" | "sshKeys" | "policies" | "tasks" | "systemSettings"> = {
  nodes: "nodes",
  ssh_keys: "sshKeys",
  policies: "policies",
  tasks: "tasks",
  system_settings: "systemSettings",
};

export type ConfigImportPreviewPhase =
  | { status: "loading" }
  | { status: "error"; message: string; blocked: boolean }
  | { status: "ready"; preview: ConfigImportPreview };

type ConfigImportPreviewDialogProps = {
  open: boolean;
  fileName: string;
  conflict: ConfigImportConflict;
  phase: ConfigImportPreviewPhase;
  canContinue: boolean;
  totpUnavailable: boolean;
  onConflictChange: (conflict: ConfigImportConflict) => void;
  onRetry: () => void;
  onCancel: () => void;
  onContinue: () => void;
  onOpenChange: (open: boolean) => void;
};

function CountCell({ label, value, emphasis }: { label: string; value: number; emphasis: "critical" | "warning" | "quiet" }) {
  return (
    <p className={cn(
      "min-w-0 rounded-md border px-2 py-1.5",
      emphasis === "critical" && "border-destructive/40 bg-destructive/10",
      emphasis === "warning" && "border-warning/40 bg-warning/10",
      emphasis === "quiet" && "border-border/70",
    )}>
      <span className="block truncate text-[11px] text-muted-foreground">{label}</span>
      <span className={cn(
        "tabular-nums",
        emphasis === "quiet" ? "text-sm font-medium text-foreground" : "text-lg font-semibold",
        emphasis === "critical" && "text-destructive",
        emphasis === "warning" && "text-warning",
      )}>{value}</span>
    </p>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-2">
      <h3 className="text-xs font-medium text-foreground">{title}</h3>
      {children}
    </section>
  );
}

function PreviewReady({ preview }: { preview: ConfigImportPreview }) {
  const { t } = useTranslation();
  const rejected = preview.entities.reduce((total, entity) => total + entity.rejected, 0);
  const pending = preview.entities.reduce((total, entity) => total + entity.pendingValidation, 0);
  const unknownAssets = preview.assetGraph.some((asset) => asset.status === "unknown");
  const unsupportedAssets = preview.assetGraph.some((asset) => asset.status === "unsupported");
  const caution = rejected > 0 || pending > 0 || unknownAssets || unsupportedAssets
    || preview.warnings.length > 0
    || preview.warningsTruncated > 0
    || preview.pendingChecksTruncated > 0;

  return (
    <div className="space-y-4">
      <InlineAlert tone={caution ? "warning" : "info"} live={false} title={t("configExport.preview.currentSnapshot")}>
        <p>{t("configExport.preview.notLocked")}</p>
      </InlineAlert>

      <Section title={t("configExport.preview.entityCounts")}>
        <div className="space-y-2">
          {PREVIEW_ENTITIES.map((entity) => {
            const counts = preview.entities.find((item) => item.entity === entity);
            if (!counts) return null;
            return (
              <div key={entity} className="rounded-md border border-border/70 p-2">
                <p className="mb-1.5 text-xs font-medium text-foreground">{t(`configExport.result.${PREVIEW_ENTITY_LABEL[entity]}`)}</p>
                <div className="grid grid-cols-2 gap-1.5 min-[420px]:grid-cols-4">
                  <CountCell label={t("configExport.preview.input")} value={counts.input} emphasis="quiet" />
                  <CountCell label={t("configExport.result.created")} value={counts.created} emphasis="quiet" />
                  <CountCell label={t("configExport.result.updated")} value={counts.updated} emphasis="quiet" />
                  <CountCell label={t("configExport.result.skipped")} value={counts.skipped} emphasis="quiet" />
                  <CountCell label={t("configExport.result.rejected")} value={counts.rejected} emphasis={counts.rejected > 0 ? "critical" : "quiet"} />
                  <CountCell label={t("configExport.preview.pendingValidation")} value={counts.pendingValidation} emphasis={counts.pendingValidation > 0 ? "warning" : "quiet"} />
                  <CountCell label={t("configExport.preview.disabledImported")} value={counts.disabledImported} emphasis={counts.disabledImported > 0 ? "warning" : "quiet"} />
                </div>
              </div>
            );
          })}
        </div>
      </Section>

      <Section title={t("configExport.result.systemSettings")}>
        <InlineAlert tone={preview.systemSettings.willWrite ? "warning" : "info"} live={false}>
          <p>{t(preview.systemSettings.willWrite ? "configExport.preview.settingsWillWrite" : "configExport.preview.settingsNoWrite")}</p>
          {preview.systemSettings.willWrite ? (
            <p className="mt-1">{t("configExport.preview.settingsCounts", {
              created: preview.systemSettings.created,
              updated: preview.systemSettings.updated,
            })}</p>
          ) : null}
          {preview.systemSettings.willWrite && preview.systemSettings.appliesInSkip ? (
            <p className="mt-1">{t("configExport.preview.settingsAppliesInSkip")}</p>
          ) : null}
        </InlineAlert>
      </Section>

      {rejected > 0 ? (
        <InlineAlert tone="warning" live={false}>
          <p>{t("configExport.preview.partialRejected")}</p>
        </InlineAlert>
      ) : null}

      {preview.warnings.length > 0 || preview.warningsTruncated > 0 ? (
        <Section title={t("configExport.preview.warningsTitle")}>
          {preview.warnings.length > 0 ? (
            <ul className="space-y-1 text-xs" aria-label={t("configExport.result.warningList")}>
              {preview.warnings.map((warning, position) => (
                <li key={`${warning.entity}:${warning.index}:${warning.code}:${position}`} className="break-words">
                  {t(`configExport.entity.${warning.entity}`)}
                  {" "}
                  {t("configExport.warning.index", { index: warning.index })}
                  {warning.name ? ` (${warning.name})` : ""}
                  {". "}
                  {t(`configExport.warning.${warning.code}`)}
                </li>
              ))}
            </ul>
          ) : null}
          {preview.warningsTruncated > 0 ? (
            <p className="text-xs">{t("configExport.result.warningsTruncated", { count: preview.warningsTruncated })}</p>
          ) : null}
        </Section>
      ) : null}

      {preview.pendingChecks.length > 0 || preview.pendingChecksTruncated > 0 ? (
        <Section title={t("configExport.preview.pendingList")}>
          {preview.pendingChecks.length > 0 ? (
            <ul className="space-y-1 text-xs" aria-label={t("configExport.preview.pendingList")}>
              {preview.pendingChecks.map((check, position) => (
                <li key={`${check.entity}:${check.index}:${check.code}:${position}`} className="break-words">
                  {t(`configExport.entity.${check.entity}`)}
                  {" "}
                  {t("configExport.warning.index", { index: check.index })}
                  {". "}
                  {t(`configExport.preview.pendingCode.${check.code}`)}
                </li>
              ))}
            </ul>
          ) : null}
          {preview.pendingChecksTruncated > 0 ? (
            <p className="text-xs">{t("configExport.preview.pendingTruncated", { count: preview.pendingChecksTruncated })}</p>
          ) : null}
        </Section>
      ) : null}

      {preview.secrets.length > 0 || preview.secretsTruncated > 0 ? (
        <Section title={t("configExport.preview.secretsTitle")}>
          {preview.secrets.length > 0 ? (
            <ul className="space-y-2 text-xs" aria-label={t("configExport.preview.secretsTitle")}>
              {preview.secrets.map((secret, position) => (
                <li key={`${secret.entity}:${secret.index}:${secret.field}:${position}`} className="break-words">
                  <span>
                    {t(`configExport.entity.${secret.entity}`)}
                    {" "}
                    {t("configExport.warning.index", { index: secret.index })}
                    {". "}
                    {t(`configExport.preview.secretField.${secret.field}`)}
                  </span>
                  <span className="mt-0.5 block">
                    {t("configExport.preview.secretPresence.label")}
                    {": "}
                    {t(secret.present
                      ? "configExport.preview.secretPresence.provided"
                      : "configExport.preview.secretPresence.notProvided")}
                  </span>
                  <span className="block">
                    {t("configExport.preview.secretOutcome")}
                    {": "}
                    {t(`configExport.preview.secretAction.${secret.action}`)}
                  </span>
                </li>
              ))}
            </ul>
          ) : null}
          {preview.secretsTruncated > 0 ? (
            <p className="text-xs">{t("configExport.preview.secretsTruncated", { count: preview.secretsTruncated })}</p>
          ) : null}
        </Section>
      ) : null}

      <Section title={t("configExport.preview.assetsTitle")}>
        <ul className="space-y-1 text-xs" aria-label={t("configExport.preview.assetsTitle")}>
          {preview.assetGraph.map((asset) => (
            <li key={asset.entity} className="break-words">
              {t(`configExport.preview.assetEntity.${asset.entity}`)}
              {": "}
              {asset.input}
              {". "}
              {t(`configExport.preview.assetStatus.${asset.status}`)}
            </li>
          ))}
        </ul>
        {unknownAssets ? <p className="text-xs text-warning">{t("configExport.preview.assetsUnknown")}</p> : null}
        {unsupportedAssets ? <p className="text-xs text-warning">{t("configExport.preview.assetsUnsupported")}</p> : null}
      </Section>
    </div>
  );
}

export function ConfigImportPreviewDialog({
  open,
  fileName,
  conflict,
  phase,
  canContinue,
  totpUnavailable,
  onConflictChange,
  onRetry,
  onCancel,
  onContinue,
  onOpenChange,
}: ConfigImportPreviewDialogProps) {
  const { t } = useTranslation();
  const continueDisabled = totpUnavailable || !canContinue || phase.status !== "ready";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{t("configExport.preview.title")}</DialogTitle>
          <DialogDescription>{t("configExport.preview.description")}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <p className="break-all text-xs text-muted-foreground">
            <span className="font-medium text-foreground">{t("configExport.preview.fileName")}</span>
            {": "}
            {fileName}
          </p>
          <div className="space-y-1.5">
            <label className="text-sm font-medium" htmlFor="config-import-preview-conflict">
              {t("configExport.preview.conflictLabel")}
            </label>
            <Select
              id="config-import-preview-conflict"
              value={conflict}
              onChange={(event) => onConflictChange(event.target.value === "overwrite" ? "overwrite" : "skip")}
            >
              <option value="skip">{t("configExport.preview.conflictSkip")}</option>
              <option value="overwrite">{t("configExport.preview.conflictOverwrite")}</option>
            </Select>
            <p className="text-xs text-muted-foreground">
              {t(conflict === "overwrite" ? "configExport.preview.conflictOverwriteHint" : "configExport.preview.conflictSkipHint")}
            </p>
          </div>

          {totpUnavailable ? (
            <div id="config-import-preview-prerequisite">
              <StepUpPrerequisiteNotice />
            </div>
          ) : null}

          {phase.status === "loading" ? (
            <div className="flex items-start gap-2 rounded-md border border-border/70 px-3 py-2" role="status" aria-live="polite">
              <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin" aria-hidden />
              <div>
                <p className="text-sm font-medium">{t("configExport.preview.loading")}</p>
                <p className="text-xs text-muted-foreground">{t("configExport.preview.loadingHint")}</p>
              </div>
            </div>
          ) : null}

          {phase.status === "error" ? (
            <InlineAlert tone="critical" title={t("configExport.preview.errorTitle")}>
              <p>{phase.message}</p>
              {phase.blocked ? <p className="mt-1">{t("configExport.preview.blockedContinue")}</p> : null}
            </InlineAlert>
          ) : null}

          {phase.status === "ready" ? <PreviewReady preview={phase.preview} /> : null}
        </DialogBody>
        <DialogFooter className="flex-wrap">
          <Button type="button" variant="outline" className="h-auto min-h-9 max-w-full whitespace-normal" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          {phase.status === "error" && !phase.blocked ? (
            <Button type="button" variant="outline" className="h-auto min-h-9 max-w-full whitespace-normal" onClick={onRetry}>
              {t("configExport.preview.retry")}
            </Button>
          ) : null}
          <Button
            type="button"
            className="h-auto min-h-9 max-w-full whitespace-normal"
            onClick={continueDisabled ? undefined : onContinue}
            disabled={continueDisabled}
            aria-describedby={totpUnavailable ? "config-import-preview-prerequisite" : undefined}
          >
            {t("configExport.preview.continue")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
