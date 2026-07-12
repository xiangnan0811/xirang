import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Shield, Trash2 } from "lucide-react";
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
import { useAuth } from "@/context/auth-context.hooks";
import { useConfirm } from "@/hooks/use-confirm";
import {
  createCredentialsApi,
  type AppCredential,
} from "@/lib/api/credentials";
import { getErrorMessage } from "@/lib/utils";

export function CredentialsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { confirm, dialog } = useConfirm();

  const [credentials, setCredentials] = useState<AppCredential[]>([]);
  const [loading, setLoading] = useState(true);

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingCredential, setEditingCredential] =
    useState<AppCredential | null>(null);

  const fetchCredentials = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const data = await createCredentialsApi().list(token);
      setCredentials(data);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    fetchCredentials();
  }, [fetchCredentials]);

  const openCreateDialog = () => {
    setEditingCredential(null);
    setEditorOpen(true);
  };

  const openEditDialog = (cred: AppCredential) => {
    setEditingCredential(cred);
    setEditorOpen(true);
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
          ) : (
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
          {loading ? (
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
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border bg-muted/50">
                    <th className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("common.name")}
                    </th>
                    <th className="px-4 py-3 text-left font-medium text-muted-foreground">
                      {t("common.type")}
                    </th>
                    <th className="hidden px-4 py-3 text-left font-medium text-muted-foreground md:table-cell">
                      {t("common.description")}
                    </th>
                    <th className="hidden px-4 py-3 text-center font-medium text-muted-foreground sm:table-cell">
                      {t("credentials.password")}
                    </th>
                    <th className="hidden px-4 py-3 text-center font-medium text-muted-foreground sm:table-cell">
                      {t("credentials.references")}
                    </th>
                    <th className="px-4 py-3 text-right font-medium text-muted-foreground">
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
                      <td className="px-4 py-3 font-medium">{cred.name}</td>
                      <td className="px-4 py-3">
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
                      <td className="hidden px-4 py-3 text-center sm:table-cell">
                        {cred.referenceCount > 0 ? (
                          <Badge tone="info">{cred.referenceCount}</Badge>
                        ) : (
                          <span className="text-muted-foreground">0</span>
                        )}
                      </td>
                      <td className="px-4 py-3 text-right">
                        <div className="inline-flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t("common.edit")}
                            onClick={() => openEditDialog(cred)}
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
        editingCredential={editingCredential}
        onSaved={fetchCredentials}
      />

      {/* ── Delete Confirmation (from useConfirm hook) ── */}
      {dialog}
    </div>
  );
}
