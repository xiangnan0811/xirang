import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Copy, Plus, Trash2, Wrench } from "lucide-react";
import { useSharedContext } from "@/context/shared-context";
import { useNodesContext } from "@/context/nodes-context";
import { usePoliciesContext } from "@/context/policies-context";
import {
  PolicyEditorDialog,
  type PolicyDraft,
} from "@/components/policy-editor-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { Pagination } from "@/components/ui/pagination";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { useClientPagination } from "@/hooks/use-client-pagination";
import { usePageFilters } from "@/hooks/use-page-filters";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { NewPolicyInput, PolicyRecord } from "@/types/domain";
import { useAuth } from "@/context/auth-context";
import { PolicyCard } from "@/pages/policies-page.card";
import { PoliciesFilters } from "@/pages/policies-page.filters";

const keywordStorageKey = "xirang.policies.keyword";

export function PoliciesPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { loading, globalSearch, setGlobalSearch } = useSharedContext();
  const { nodes, refreshNodes } = useNodesContext();
  const {
    policies,
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
    refreshPolicies,
  } = usePoliciesContext();

  useEffect(() => {
    void refreshPolicies();
    void refreshNodes();
  }, [refreshPolicies, refreshNodes]);

  const {
    keyword,
    setKeyword,
    deferredKeyword,
    reset: resetFilters,
  } = usePageFilters({
    keyword: { key: keywordStorageKey, default: "" },
  }, globalSearch, setGlobalSearch);

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingPolicy, setEditingPolicy] = useState<PolicyRecord | null>(null);
  const [selectedPolicyIds, setSelectedPolicyIds] = useState<number[]>([]);
  const { confirm, dialog } = useConfirm();

  const togglePolicySelection = (id: number, checked: boolean) => {
    setSelectedPolicyIds((prev) =>
      checked ? (prev.includes(id) ? prev : [...prev, id]) : prev.filter((pid) => pid !== id)
    );
  };

  const handleBatchToggle = async (enabled: boolean) => {
    if (!selectedPolicyIds.length || !token) return;
    try {
      await apiClient.batchTogglePolicies(token, selectedPolicyIds, enabled);
      toast.success(t('policies.batchToggleSuccess', { action: enabled ? t('common.enable') : t('common.disable'), count: selectedPolicyIds.length }));
      setSelectedPolicyIds([]);
      void refreshPolicies();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const filteredPolicies = useMemo(() => {
    const effectiveKeyword = deferredKeyword.trim().toLowerCase();
    if (!effectiveKeyword) {
      return policies;
    }
    return policies.filter((policy) =>
      `${policy.name} ${policy.sourcePath} ${policy.targetPath} ${policy.cron}`
        .toLowerCase()
        .includes(effectiveKeyword)
    );
  }, [deferredKeyword, policies]);

  const {
    pagedItems: pagedPolicies,
    page,
    pageSize,
    total: filteredTotal,
    setPage,
    setPageSize,
  } = useClientPagination(filteredPolicies);

  const activeCount = policies.filter((policy) => policy.enabled).length;
  const disabledCount = policies.length - activeCount;

  const openCreateDialog = () => {
    setEditingPolicy(null);
    setEditorOpen(true);
  };

  const openEditDialog = (policy: PolicyRecord) => {
    setEditingPolicy(policy);
    setEditorOpen(true);
  };

  const handleSave = async (draft: PolicyDraft) => {
    // Dialog validates required fields before calling this handler; early-return silently if bypassed
    if (!draft.name.trim() || !draft.sourcePath.trim() || !draft.cron.trim()) return;

    const input: NewPolicyInput = {
      name: draft.name.trim(),
      sourcePath: draft.sourcePath.trim(),
      targetPath: (draft.targetPath || "/backup").trim(),
      cron: draft.cron.trim(),
      criticalThreshold: Math.max(1, Number(draft.criticalThreshold || 1)),
      enabled: draft.enabled,
      nodeIds: draft.nodeIds ?? [],
      verifyEnabled: draft.verifyEnabled ?? false,
      verifySampleRate: draft.verifySampleRate ?? 0,
    };

    try {
      if (draft.id) {
        await updatePolicy(draft.id, input);
        toast.success(t('policies.updateSuccess', { name: draft.name }));
      } else {
        await createPolicy(input);
        toast.success(t('policies.createSuccess', { name: draft.name }));
      }

      setEditorOpen(false);
      setEditingPolicy(null);
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const onDelete = async (policy: PolicyRecord) => {
    const ok = await confirm({
      title: t('policies.confirmDelete'),
      description: t('policies.confirmDeleteDesc', { name: policy.name }),
    });
    if (!ok) {
      return;
    }
    try {
      await deletePolicy(policy.id);
      toast.success(t('policies.deleteSuccess', { name: policy.name }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const onTogglePolicy = async (policy: PolicyRecord) => {
    try {
      await togglePolicy(policy.id);
      toast.success(t('policies.toggleSuccess', { name: policy.name, action: policy.enabled ? t('common.disable') : t('common.enable') }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const onCloneFromTemplate = async (policy: PolicyRecord) => {
    if (!token) return;
    try {
      await apiClient.clonePolicyFromTemplate(token, policy.id);
      toast.success(t('policies.cloneSuccess', { name: policy.name }));
      void refreshPolicies();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="rounded-lg border border-border bg-card">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={openCreateDialog}>
                <Plus className="mr-1 size-3.5" />
                {t('policies.addPolicy')}
              </Button>
              {selectedPolicyIds.length > 0 && (
                <>
                  <Button size="sm" variant="outline" onClick={() => void handleBatchToggle(true)}>
                    {t('policies.batchEnableCount', { count: selectedPolicyIds.length })}
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => void handleBatchToggle(false)}>
                    {t('policies.batchDisableCount', { count: selectedPolicyIds.length })}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setSelectedPolicyIds([])}>
                    {t('policies.clearSelection')}
                  </Button>
                </>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone="success">{t('policies.enabledCount', { count: activeCount })}</Badge>
              <Badge tone="neutral">{t('policies.disabledCount', { count: disabledCount })}</Badge>
              <Badge tone="neutral" className="hidden lg:inline-flex">{t('policies.filteredCount', { count: filteredPolicies.length })}</Badge>
            </div>
          </div>
          <PoliciesFilters
            keyword={keyword}
            setKeyword={setKeyword}
            activeCount={activeCount}
            totalCount={policies.length}
            resetFilters={resetFilters}
          />

          {loading ? (
            <LoadingState
              title={t('policies.loadingTitle')}
              description={t('policies.loadingDesc')}
              rows={3}
            />
          ) : null}

          {/* 小屏卡片，大屏表格 */}
          <div className="grid gap-3 sm:grid-cols-2 md:hidden">
            {pagedPolicies.map((policy) => (
              <PolicyCard
                key={policy.id}
                policy={policy}
                nodes={nodes}
                selected={selectedPolicyIds.includes(policy.id)}
                onToggleSelect={togglePolicySelection}
                onEdit={openEditDialog}
                onDelete={onDelete}
                onToggle={onTogglePolicy}
                onCloneFromTemplate={onCloneFromTemplate}
              />
            ))}

            {!filteredPolicies.length ? (
              <EmptyState
                className="md:col-span-2 lg:col-span-3"
                title={t('policies.noMatchTitle')}
                description={t('policies.noMatchDesc')}
                action={(
                  <div className="flex flex-wrap items-center justify-center gap-2">
                    <Button size="sm" variant="outline" onClick={resetFilters}>
                      {t('policies.clearFilter')}
                    </Button>
                    <Button size="sm" onClick={openCreateDialog}>
                      <Plus className="mr-1 size-4" />
                      {t('policies.addPolicy')}
                    </Button>
                  </div>
                )}
              />
            ) : null}
            <Pagination
              className="col-span-full"
              page={page}
              pageSize={pageSize}
              total={filteredTotal}
              onPageChange={setPage}
              onPageSizeChange={setPageSize}
            />
          </div>

          <div className="rounded-lg border border-border bg-card hidden overflow-x-auto md:block">
            <table className="min-w-[980px] text-left text-sm">
              <thead>
                <tr className="border-b border-border bg-secondary text-[11px] uppercase tracking-wide text-muted-foreground">
                  <th scope="col" className="w-10 px-3 py-2.5">
                    <input
                      type="checkbox"
                      className="size-4 accent-primary rounded-sm"
                      checked={pagedPolicies.length > 0 && pagedPolicies.every((p) => selectedPolicyIds.includes(p.id))}
                      onChange={(e) => {
                        if (e.target.checked) {
                          setSelectedPolicyIds((prev) => Array.from(new Set([...prev, ...pagedPolicies.map((p) => p.id)])));
                        } else {
                          const pagedIds = new Set(pagedPolicies.map((p) => p.id));
                          setSelectedPolicyIds((prev) => prev.filter((id) => !pagedIds.has(id)));
                        }
                      }}
                      aria-label={t('policies.selectAllAriaLabel')}
                    />
                  </th>
                  <th scope="col" className="px-3 py-2.5">{t('policies.columnName')}</th>
                  <th scope="col" className="px-3 py-2.5">{t('policies.columnCron')}</th>
                  <th scope="col" className="px-3 py-2.5">{t('policies.columnSource')}</th>
                  <th scope="col" className="px-3 py-2.5">{t('policies.columnTarget')}</th>
                  <th scope="col" className="px-3 py-2.5">{t('policies.columnNodes')}</th>
                  <th scope="col" className="px-3 py-2.5">{t('policies.columnStatus')}</th>
                  <th scope="col" className="px-3 py-2.5 text-right">{t('policies.columnActions')}</th>
                </tr>
              </thead>
              <tbody>
                {filteredPolicies.length ? (
                  pagedPolicies.map((policy) => (
                    <tr key={policy.id} className="border-b border-border transition-colors duration-200 ease-out hover:bg-accent">
                      <td className="px-3 py-2.5">
                        <input
                          type="checkbox"
                          className="size-4 accent-primary rounded-sm"
                          checked={selectedPolicyIds.includes(policy.id)}
                          onChange={(e) => togglePolicySelection(policy.id, e.target.checked)}
                          aria-label={t('policies.selectAriaLabel', { name: policy.name })}
                        />
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex items-center gap-1.5">
                          <p className="font-medium">{policy.name}</p>
                          {policy.isTemplate && <Badge tone="neutral">{t('policies.template')}</Badge>}
                        </div>
                        <p className="text-xs text-muted-foreground">{t('policies.threshold', { value: policy.criticalThreshold })}</p>
                      </td>
                      <td className="px-3 py-2.5">
                        <p className="font-mono text-xs">{policy.cron}</p>
                        <p className="text-xs text-muted-foreground">{policy.naturalLanguage}</p>
                      </td>
                      <td className="px-3 py-2.5 text-muted-foreground">{policy.sourcePath}</td>
                      <td className="px-3 py-2.5 text-muted-foreground">{policy.targetPath}</td>
                      <td className="px-3 py-2.5 text-muted-foreground">
                        {t('policies.nodeCount', { selected: policy.nodeIds?.length ?? 0, total: nodes?.length ?? 0 })}
                      </td>
                      <td className="px-3 py-2.5">
                        <Switch
                          checked={policy.enabled}
                          aria-label={t('policies.toggleAriaLabel', { action: policy.enabled ? t('common.disable') : t('common.enable'), name: policy.name })}
                          onCheckedChange={() => void onTogglePolicy(policy)}
                        />
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        <div className="flex items-center justify-end gap-1">
                          {policy.isTemplate && (
                            <Button
                              variant="ghost"
                              size="icon"
                              className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                              onClick={() => void onCloneFromTemplate(policy)}
                              aria-label={t('policies.cloneAriaLabel', { name: policy.name })}
                            >
                              <Copy className="size-4" />
                            </Button>
                          )}
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                            onClick={() => openEditDialog(policy)}
                            aria-label={t('policies.editAriaLabel')}
                          >
                            <Wrench className="size-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                            aria-label={t('policies.deleteAriaLabel', { name: policy.name })}
                            onClick={() => onDelete(policy)}
                          >
                            <Trash2 className="size-4" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td colSpan={8} className="px-3 py-5">
                      <EmptyState
                        className="py-8"
                        title={t('policies.noMatchTitle')}
                        description={t('policies.noMatchDescAlt')}
                        action={(
                          <div className="flex flex-wrap items-center justify-center gap-2">
                            <Button size="sm" variant="outline" onClick={resetFilters}>
                              {t('policies.clearFilter')}
                            </Button>
                            <Button size="sm" onClick={openCreateDialog}>
                              <Plus className="mr-1 size-4" />
                              {t('policies.addPolicy')}
                            </Button>
                          </div>
                        )}
                      />
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
            <Pagination
              className="mt-3 px-3 pb-2"
              page={page}
              pageSize={pageSize}
              total={filteredTotal}
              onPageChange={setPage}
              onPageSizeChange={setPageSize}
            />
          </div>
        </CardContent>
      </Card>

      <PolicyEditorDialog
        open={editorOpen}
        onOpenChange={(open) => {
          setEditorOpen(open);
          if (!open) setEditingPolicy(null);
        }}
        editingPolicy={editingPolicy}
        onSave={handleSave}
        nodes={nodes}
      />

      {dialog}
    </div>
  );
}
