import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Shield, Trash2 } from "lucide-react";
import { Navigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
} from "@/components/ui/data-surface";
import { PageHero } from "@/components/ui/page-hero";
import { toast } from "@/components/ui/toast-sonner";
import { CredentialEditorDialog } from "@/components/credential-editor-dialog";
import { CredentialReferencesDialog } from "@/components/credential-references-dialog";
import { InventoryRetryAlert } from "@/components/ui/inventory-retry-alert";
import {
  dialogOpenerFromTarget,
  restoreConnectedDialogOpener,
} from "@/components/ui/dialog-opener";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { useAuth } from "@/context/auth-context.hooks";
import { useConfirm } from "@/hooks/use-confirm";
import {
  createCredentialsApi,
  type AppCredential,
} from "@/lib/api/credentials";
import { getErrorMessage } from "@/lib/utils";

export function CredentialsPage() {
  const { token, role, authTransitioning } = useAuth();
  if (authTransitioning) return null;
  if (role !== "admin") return <Navigate to="/app/overview" replace />;
  return <CredentialsPageContent key={token ?? ""} />;
}

function CredentialsPageContent() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { confirm, dialog } = useConfirm();

  const [credentials, setCredentials] = useState<AppCredential[]>([]);
  const [loading, setLoading] = useState(Boolean(token));
  const [listError, setListError] = useState<string | null>(null);

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingCredential, setEditingCredential] =
    useState<AppCredential | null>(null);
  const editorOpenerRef = useRef<HTMLElement | null>(null);
  const editorOnCloseAutoFocus: DialogCloseAutoFocus = (event) => {
    restoreConnectedDialogOpener(event, editorOpenerRef.current);
  };

  const referencesOpenerRef = useRef<HTMLElement | null>(null);
  const [referencesOpen, setReferencesOpen] = useState(false);
  const [referencesCredential, setReferencesCredential] = useState<{ id: number; name: string } | null>(null);
  const referencesOnCloseAutoFocus: DialogCloseAutoFocus = (event) => {
    const opener = referencesOpenerRef.current;
    if (!opener?.isConnected) {
      event.preventDefault();
      return;
    }
    restoreConnectedDialogOpener(event, opener);
  };

  const [requestVersion, setRequestVersion] = useState(0);
  const fetchCredentials = useCallback(() => {
    if (!token) return;
    setCredentials([]);
    setListError(null);
    setLoading(true);
    setRequestVersion((version) => version + 1);
  }, [token]);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    createCredentialsApi().list(token, controller.signal)
      .then((data) => {
        if (controller.signal.aborted) return;
        setCredentials(data);
        setListError(null);
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setCredentials([]);
        setListError(getErrorMessage(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [token, requestVersion]);

  const openCreateDialog = (event?: { currentTarget: EventTarget | null }) => {
    editorOpenerRef.current = dialogOpenerFromTarget(event?.currentTarget);
    setEditingCredential(null);
    setEditorOpen(true);
  };

  const openEditDialog = (cred: AppCredential, opener?: EventTarget | null) => {
    editorOpenerRef.current = dialogOpenerFromTarget(opener);
    setEditingCredential(cred);
    setEditorOpen(true);
  };

  const openReferencesDialog = (cred: AppCredential, opener?: EventTarget | null) => {
    referencesOpenerRef.current = dialogOpenerFromTarget(opener);
    setReferencesCredential({ id: cred.id, name: cred.name });
    setReferencesOpen(true);
  };

  const handleDelete = async (cred: AppCredential) => {
    if (!token) return;
    const ok = await confirm({
      title: t("credentials.confirmDeleteTitle"),
      description: t("credentials.confirmDeleteDesc", { name: cred.name }),
    });
    if (!ok) return;
    try {
      await createCredentialsApi().delete(token, cred.id);
      toast.success(t("credentials.deleted", { name: cred.name }));
      fetchCredentials();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  // Type display label
  const typeLabel = (type: string) => {
    const map: Record<string, string> = {
      mysql: "MySQL",
      postgresql: "PostgreSQL",
      mongodb: "MongoDB",
      redis: "Redis",
      elasticsearch: "Elasticsearch",
      smtp: "SMTP",
      restic_repo: "Restic Repo",
      docker: "Docker",
    };
    return map[type] ?? type;
  };

  const typeBadgeTone = (type: string) => {
    if (type === "docker") return "info" as const;
    if (type === "mysql" || type === "postgresql") return "success" as const;
    if (type === "redis") return "warning" as const;
    if (type === "mongodb") return "success" as const;
    if (type === "elasticsearch") return "warning" as const;
    if (type === "smtp") return "neutral" as const;
    if (type === "restic_repo") return "info" as const;
    return "neutral" as const;
  };

  const totalCredentials = credentials.length;
  const passwordConfigured = credentials.filter((cred) => cred.hasPassword).length;
  const referencedCredentials = credentials.filter(
    (cred) => cred.referenceCount > 0,
  ).length;
  const unusedCredentials = totalCredentials - referencedCredentials;
  const totalMeta = `${t("common.all")} ${totalCredentials}`;
  const passwordMeta = `${t("common.password")} ${passwordConfigured}`;
  const referencedMeta = `${t("credentials.references")} ${referencedCredentials}`;
  const unusedMeta = `${t("common.neverUsed")} ${unusedCredentials}`;

  return (
    <div className="animate-fade-in space-y-5">
      <PageHero
        title={t("credentials.pageTitle")}
        subtitle={t("credentials.pageDesc")}
        meta={
          loading ? (
            <Badge tone="neutral">{t("common.loading")}</Badge>
          ) : listError ? null : (
            <>
              <Badge tone="info">{totalMeta}</Badge>
              <Badge tone={passwordConfigured > 0 ? "success" : "neutral"}>
                {passwordMeta}
              </Badge>
              <Badge tone={referencedCredentials > 0 ? "info" : "neutral"}>
                {referencedMeta}
              </Badge>
              <Badge tone={unusedCredentials > 0 ? "warning" : "neutral"}>
                {unusedMeta}
              </Badge>
            </>
          )
        }
        actions={
          <Button onClick={openCreateDialog}>
            <Shield className="mr-1.5 size-4" aria-hidden="true" />
            {t("credentials.createBtn")}
          </Button>
        }
      />

      <DataSurface>
        <DataSurfaceHeader
          title={t("credentials.surfaceTitle")}
          description={t("credentials.pageDesc")}
        />
        <DataSurfaceContent className="p-0">
          {listError ? (
            <div className="p-4">
              <InventoryRetryAlert error={listError} onRetry={fetchCredentials} />
            </div>
          ) : loading ? (
            <div
              className="flex items-center justify-center py-16"
              role="status"
              aria-label={t("common.loading")}
            >
              <div className="size-6 animate-spin rounded-full border-2 border-muted-foreground border-t-transparent" />
            </div>
          ) : credentials.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
              <Shield className="mb-3 size-10 opacity-30" aria-hidden="true" />
              <p className="text-sm">{t("credentials.empty")}</p>
              <p className="mt-1 max-w-md text-center text-xs">
                {t("credentials.emptyDesc")}
              </p>
              <Button
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={openCreateDialog}
              >
                {t("credentials.createBtn")}
              </Button>
            </div>
          ) : (
            <div className="min-w-0">
              <table className="w-full table-fixed text-sm sm:table-auto">
                <thead>
                  <tr className="border-b border-border bg-muted/50">
                    <th className="px-2 py-3 text-left font-medium text-muted-foreground sm:px-4">
                      {t("common.name")}
                    </th>
                    <th className="hidden px-2 py-3 text-left font-medium text-muted-foreground sm:table-cell sm:px-4">
                      {t("common.type")}
                    </th>
                    <th className="hidden px-4 py-3 text-left font-medium text-muted-foreground md:table-cell">
                      {t("common.description")}
                    </th>
                    <th className="hidden px-4 py-3 text-center font-medium text-muted-foreground sm:table-cell">
                      {t("credentials.password")}
                    </th>
                    <th className="w-16 px-2 py-3 text-center font-medium text-muted-foreground sm:w-auto sm:px-4">
                      {t("credentials.references")}
                    </th>
                    <th className="w-24 px-2 py-3 text-right font-medium text-muted-foreground sm:w-auto sm:px-4">
                      {t("common.actions")}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {credentials.map((cred) => (
                    <tr
                      key={cred.id}
                      className="border-b border-border transition-colors hover:bg-muted/30"
                    >
                      <td className="overflow-hidden px-2 py-2 font-medium sm:px-4 sm:py-3">
                        <div className="truncate" title={cred.name}>{cred.name}</div>
                      </td>
                      <td className="hidden overflow-hidden px-2 py-3 sm:table-cell sm:px-4">
                        <Badge tone={typeBadgeTone(cred.type)}>
                          {typeLabel(cred.type)}
                        </Badge>
                      </td>
                      <td className="hidden max-w-[200px] truncate px-4 py-3 text-muted-foreground md:table-cell">
                        {cred.description || "—"}
                      </td>
                      <td className="hidden px-4 py-3 text-center sm:table-cell">
                        {cred.hasPassword ? (
                          <Badge tone="success">
                            {t("credentials.configured")}
                          </Badge>
                        ) : (
                          <Badge tone="neutral">
                            {t("credentials.none")}
                          </Badge>
                        )}
                      </td>
                      <td className="px-2 py-2 text-center sm:px-4 sm:py-3">
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          className="h-11 min-w-11 px-2 sm:h-8 sm:min-w-8"
                          aria-label={t("credentials.referencesOpen", {
                            name: cred.name,
                            count: cred.referenceCount,
                          })}
                          onClick={(event) => openReferencesDialog(cred, event.currentTarget)}
                        >
                          <Badge tone={cred.referenceCount > 0 ? "info" : "neutral"} dot={false}>
                            {cred.referenceCount}
                          </Badge>
                        </Button>
                      </td>
                      <td className="whitespace-nowrap px-2 py-2 text-right sm:px-4 sm:py-3">
                        <div className="inline-flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("common.edit")}
                            onClick={(event) => openEditDialog(cred, event.currentTarget)}
                          >
                            <Pencil className="size-3.5" aria-hidden="true" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("common.delete")}
                            onClick={() => handleDelete(cred)}
                          >
                            <Trash2
                              className="size-3.5 text-destructive"
                              aria-hidden="true"
                            />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </DataSurfaceContent>
      </DataSurface>

      {/* ── Editor Dialog ── */}
      <CredentialEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        onCloseAutoFocus={editorOnCloseAutoFocus}
        editingCredential={editingCredential}
        onSaved={fetchCredentials}
      />

      <CredentialReferencesDialog
        open={referencesOpen}
        onOpenChange={setReferencesOpen}
        credential={referencesCredential}
        onCloseAutoFocus={referencesOnCloseAutoFocus}
      />

      {/* ── Delete Confirmation (from useConfirm hook) ── */}
      {dialog}
    </div>
  );
}
