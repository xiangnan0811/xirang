import { type FormEvent, useRef, useState } from "react";
import { Download, Upload, Loader2, ShieldAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { useConfirm } from "@/hooks/use-confirm";
import { getErrorMessage } from "@/lib/utils";
import { toast } from "sonner";

const CONFIG_GRANT_MAX_REASON_LENGTH = 240;
const CONFIG_GRANT_TTL_SECONDS = 600;

type ConfigGrantMode = "import" | "sensitive-export";

export function ConfigExportImport() {
  const { t } = useTranslation();
  const { token, role, ensureStepUpProof } = useAuth();
  const { confirm, dialog } = useConfirm();
  const [exporting, setExporting] = useState(false);
  const [importing, setImporting] = useState(false);
  const [sensitiveExporting, setSensitiveExporting] = useState(false);
  const [grantDialogOpen, setGrantDialogOpen] = useState(false);
  const [grantMode, setGrantMode] = useState<ConfigGrantMode>("import");
  const [grantReason, setGrantReason] = useState("");
  const [grantError, setGrantError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  if (!token || role !== "admin") return null;

  const downloadConfigPayload = (data: unknown, suffix = "config") => {
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `xirang-${suffix}-${new Date().toISOString().slice(0, 10)}.json`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const handleExport = async () => {
    setExporting(true);
    try {
      const data = await apiClient.exportConfig(token);
      downloadConfigPayload(data, "config");
      toast.success(t('configExport.exportSuccess'));
    } catch (err) {
      toast.error(getErrorMessage(err, t('configExport.exportFailed')));
    } finally {
      setExporting(false);
    }
  };

  const resetGrantPromptState = () => {
    setGrantReason("");
    setGrantError(null);
    setGrantDialogOpen(false);
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const handleImportClick = () => {
    fileInputRef.current?.click();
  };

  const handleSensitiveExportClick = () => {
    setGrantMode("sensitive-export");
    setGrantReason("");
    setGrantError(null);
    setGrantDialogOpen(true);
  };

  const handleFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const ok = await confirm({
      title: t("configExport.importConfirm"),
      description: t("common.irreversible"),
    });
    if (!ok) {
      resetGrantPromptState();
      return;
    }
    setGrantMode("import");
    setGrantReason("");
    setGrantError(null);
    setGrantDialogOpen(true);
  };

  const grantSubmitting = importing || sensitiveExporting;

  const handleGrantDialogChange = (open: boolean) => {
    if (grantSubmitting) return;
    if (!open) {
      resetGrantPromptState();
      return;
    }
    setGrantDialogOpen(true);
  };

  const getSelectedImportFile = (): File => {
    const file = fileInputRef.current?.files?.[0];
    if (!file) {
      throw new Error(t('configExport.importPayloadMissing'));
    }
    return file;
  };

  const parseImportFile = async (file: File): Promise<Record<string, unknown>> => {
    const text = await file.text();
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      throw new Error(t('configExport.invalidImportFile'));
    }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      throw new Error(t('configExport.invalidImportFile'));
    }
    return parsed as Record<string, unknown>;
  };

  const handleImportGrantSubmit = async (reason: string) => {
    setImporting(true);
    setGrantError(null);
    try {
      const file = getSelectedImportFile();
      await parseImportFile(file);
      const proof = await ensureStepUpProof(
        STEP_UP_ACTIONS.configImport,
        { persist: false, reuseCached: false },
      );
      await apiClient.requestConfigImportCredentialGrant(token, {
        reason,
        requestedTtlSeconds: CONFIG_GRANT_TTL_SECONDS,
      }, proof);
      const pendingImport = await parseImportFile(file);
      const result = await apiClient.importConfig(token, pendingImport, "skip", proof);
      toast.success(t('configExport.importSuccess', { imported: result.imported, skipped: result.skipped }));
      resetGrantPromptState();
    } catch (err) {
      setGrantError(getErrorMessage(err, t('configExport.importFailed')));
    } finally {
      setImporting(false);
    }
  };

  const handleSensitiveExportGrantSubmit = async (reason: string) => {
    setSensitiveExporting(true);
    setGrantError(null);
    try {
      const proof = await ensureStepUpProof(
        STEP_UP_ACTIONS.configExport,
        { persist: false, reuseCached: false },
      );
      await apiClient.requestConfigExportCredentialGrant(token, {
        reason,
        requestedTtlSeconds: CONFIG_GRANT_TTL_SECONDS,
      }, proof);
      const data = await apiClient.exportConfig(token, true, proof);
      downloadConfigPayload(data, "config-with-sensitive");
      toast.success(t('configExport.sensitiveExportSuccess'));
      resetGrantPromptState();
    } catch (err) {
      setGrantError(getErrorMessage(err, t('configExport.sensitiveExportFailed')));
    } finally {
      setSensitiveExporting(false);
    }
  };

  const handleGrantSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const reason = grantReason.trim();
    if (!reason) {
      setGrantError(t('configExport.grantReasonRequired'));
      return;
    }
    if (Array.from(reason).length > CONFIG_GRANT_MAX_REASON_LENGTH) {
      setGrantError(t('configExport.grantReasonTooLong', { max: CONFIG_GRANT_MAX_REASON_LENGTH }));
      return;
    }
    if (grantMode === "sensitive-export") {
      await handleSensitiveExportGrantSubmit(reason);
      return;
    }
    await handleImportGrantSubmit(reason);
  };

  return (
    <>
      <Card className="glass-panel border-border/70 relative overflow-hidden group">
        <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" aria-hidden />
        <CardHeader className="pb-3 z-10 relative">
          <CardTitle className="text-base">{t('configExport.title')}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-muted-foreground mb-3">
            {t('configExport.desc')}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={handleExport} disabled={exporting}>
              {exporting ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden /> : <Download className="mr-1 size-3.5" aria-hidden />}
              {t('configExport.exportConfig')}
            </Button>
            <Button size="sm" variant="outline" onClick={handleSensitiveExportClick} disabled={sensitiveExporting}>
              {sensitiveExporting ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden /> : <ShieldAlert className="mr-1 size-3.5" aria-hidden />}
              {t('configExport.exportSensitiveConfig')}
            </Button>
            <Button size="sm" variant="outline" onClick={handleImportClick} disabled={grantSubmitting}>
              {importing ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden /> : <Upload className="mr-1 size-3.5" aria-hidden />}
              {t('configExport.importConfig')}
            </Button>
            <input ref={fileInputRef} type="file" accept=".json" className="hidden" aria-label={t('configExport.importConfig')} onChange={handleFileChange} />
          </div>
        </CardContent>
      </Card>

      <Dialog open={grantDialogOpen} onOpenChange={handleGrantDialogChange}>
        <DialogContent size="sm">
          <form onSubmit={handleGrantSubmit}>
            <DialogHeader>
              <DialogTitle>{t(grantMode === "sensitive-export" ? 'configExport.sensitiveGrantTitle' : 'configExport.grantTitle')}</DialogTitle>
              <DialogDescription>{t(grantMode === "sensitive-export" ? 'configExport.sensitiveGrantDescription' : 'configExport.grantDescription')}</DialogDescription>
            </DialogHeader>
            <DialogBody className="space-y-3">
              <div className="space-y-1.5">
                <label className="text-sm font-medium" htmlFor="config-import-grant-reason">
                  {t('configExport.grantReasonLabel')}
                </label>
                <Textarea
                  id="config-import-grant-reason"
                  value={grantReason}
                  onChange={(event) => setGrantReason(event.target.value)}
                  maxLength={CONFIG_GRANT_MAX_REASON_LENGTH}
                  placeholder={t('configExport.grantReasonPlaceholder')}
                  disabled={grantSubmitting}
                  aria-describedby="config-import-grant-reason-hint"
                  aria-invalid={grantError ? true : undefined}
                />
                <p id="config-import-grant-reason-hint" className="text-xs text-muted-foreground">
                  {t('configExport.grantReasonHint', { max: CONFIG_GRANT_MAX_REASON_LENGTH })}
                </p>
              </div>
              {grantError ? (
                <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive" role="alert">
                  {grantError}
                </p>
              ) : null}
            </DialogBody>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={resetGrantPromptState} disabled={grantSubmitting}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" loading={grantSubmitting}>
                {t(grantMode === "sensitive-export" ? 'configExport.sensitiveGrantSubmit' : 'configExport.grantSubmit')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      {dialog}
    </>
  );
}
