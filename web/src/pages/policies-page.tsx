import { useMemo, useState } from "react";
import { Plus, Trash2, Wrench } from "lucide-react";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import {
  PolicyEditorDialog,
  type PolicyDraft,
} from "@/components/policy-editor-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { usePageFilters } from "@/hooks/use-page-filters";
import { getErrorMessage } from "@/lib/utils";
import type { NewPolicyInput, PolicyRecord } from "@/types/domain";

const keywordStorageKey = "xirang.policies.keyword";

export function PoliciesPage() {
  const {
    policies,
    loading,
    globalSearch,
    setGlobalSearch,
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
  } = useOutletContext<ConsoleOutletContext>();

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
  const { confirm, dialog } = useConfirm();

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
    if (
      !draft.name.trim() ||
      !draft.sourcePath.trim() ||
      !draft.targetPath.trim() ||
      !draft.cron.trim()
    ) {
      toast.error("保存失败：策略名称、源路径、目标路径、Cron 必填。");
      return;
    }

    const input: NewPolicyInput = {
      name: draft.name.trim(),
      sourcePath: draft.sourcePath.trim(),
      targetPath: draft.targetPath.trim(),
      cron: draft.cron.trim(),
      criticalThreshold: Math.max(1, Number(draft.criticalThreshold || 1)),
      enabled: draft.enabled,
    };

    try {
      if (draft.id) {
        await updatePolicy(draft.id, input);
        toast.success(`策略 ${draft.name} 已更新。`);
      } else {
        await createPolicy(input);
        toast.success(`策略 ${draft.name} 已新增。`);
      }

      setEditorOpen(false);
      setEditingPolicy(null);
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const onDelete = async (policy: PolicyRecord) => {
    const ok = await confirm({
      title: "确认删除",
      description: `确认删除策略 ${policy.name} 吗？`,
    });
    if (!ok) {
      return;
    }
    try {
      await deletePolicy(policy.id);
      toast.success(`策略 ${policy.name} 已删除。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const onTogglePolicy = async (policy: PolicyRecord) => {
    try {
      await togglePolicy(policy.id);
      toast.success(`策略 ${policy.name} 已${policy.enabled ? "停用" : "启用"}。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="border-border/75">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={openCreateDialog}>
                <Plus className="mr-1 size-3.5" />
                新增策略
              </Button>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="success">启用 {activeCount}</Badge>
              <Badge variant="outline">停用 {disabledCount}</Badge>
              <Badge variant="secondary" className="hidden lg:inline-flex">筛选 {filteredPolicies.length}</Badge>
            </div>
          </div>
          <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-[1fr_auto] lg:grid-cols-[1fr_auto_auto]">
            <Input
              placeholder="搜索策略 / 路径 / cron"
              aria-label="搜索策略、路径或cron表达式"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
            />
            <Badge variant="secondary" className="hidden lg:inline-flex">
              启用 {activeCount}/{policies.length}
            </Badge>
            <Button size="sm" variant="outline" onClick={resetFilters}>
              重置
            </Button>
          </div>

          {loading ? (
            <LoadingState
              title="策略数据加载中"
              description="正在拉取策略配置与启停状态..."
              rows={3}
            />
          ) : null}

          {/* 小屏卡片，大屏表格 */}
          <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3 md:hidden">
            {filteredPolicies.map((policy) => (
              <div
                key={policy.id}
                className="interactive-surface p-4 flex flex-col"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <h3 className="font-medium">{policy.name}</h3>
                    <p className="text-xs text-muted-foreground">{policy.naturalLanguage}</p>
                  </div>
                  <Badge variant={policy.enabled ? "success" : "outline"}>
                    {policy.enabled ? "启用" : "停用"}
                  </Badge>
                </div>

                <div className="mt-3 grid gap-2 text-sm text-muted-foreground flex-1">
                  <p className="break-all">源路径：{policy.sourcePath}</p>
                  <p className="break-all">目标路径：{policy.targetPath}</p>
                </div>

                <div className="mt-3 flex flex-wrap gap-2">
                  <Badge variant="outline">Cron: {policy.cron}</Badge>
                  <Badge variant="outline">失败阈值: {policy.criticalThreshold}</Badge>
                </div>

                <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border/40 pt-3">
                  <Switch
                    checked={policy.enabled}
                    aria-label={`${policy.enabled ? "停用" : "启用"}策略 ${policy.name}`}
                    onCheckedChange={() => void onTogglePolicy(policy)}
                  />
                  <div className="flex items-center gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                      onClick={() => openEditDialog(policy)}
                      aria-label="编辑策略"
                    >
                      <Wrench className="size-4" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                      aria-label={`删除策略 ${policy.name}`}
                      onClick={() => onDelete(policy)}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </div>
              </div>
            ))}

            {!filteredPolicies.length ? (
              <EmptyState
                className="md:col-span-2 lg:col-span-3"
                title="暂无匹配策略"
                description="可调整关键词筛选，或新增策略模板。"
                action={(
                  <div className="flex flex-wrap items-center justify-center gap-2">
                    <Button size="sm" variant="outline" onClick={resetFilters}>
                      清空筛选
                    </Button>
                    <Button size="sm" onClick={openCreateDialog}>
                      <Plus className="mr-1 size-4" />
                      新增策略
                    </Button>
                  </div>
                )}
              />
            ) : null}
          </div>

          <div className="glass-panel hidden overflow-x-auto md:block">
            <table className="min-w-[980px] text-left text-sm">
              <thead>
                <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
                  <th className="px-3 py-2.5">策略名</th>
                  <th className="px-3 py-2.5">Cron</th>
                  <th className="px-3 py-2.5">源路径</th>
                  <th className="px-3 py-2.5">目标路径</th>
                  <th className="px-3 py-2.5">状态</th>
                  <th className="px-3 py-2.5 text-right">操作</th>
                </tr>
              </thead>
              <tbody>
                {filteredPolicies.length ? (
                  filteredPolicies.map((policy) => (
                    <tr key={policy.id} className="border-b border-border/60 transition-colors duration-200 ease-out hover:bg-accent/35">
                      <td className="px-3 py-2.5">
                        <p className="font-medium">{policy.name}</p>
                        <p className="text-xs text-muted-foreground">阈值 {policy.criticalThreshold}</p>
                      </td>
                      <td className="px-3 py-2.5">
                        <p className="font-mono text-xs">{policy.cron}</p>
                        <p className="text-xs text-muted-foreground">{policy.naturalLanguage}</p>
                      </td>
                      <td className="px-3 py-2.5 text-muted-foreground">{policy.sourcePath}</td>
                      <td className="px-3 py-2.5 text-muted-foreground">{policy.targetPath}</td>
                      <td className="px-3 py-2.5">
                        <Switch
                          checked={policy.enabled}
                          aria-label={`${policy.enabled ? "停用" : "启用"}策略 ${policy.name}`}
                          onCheckedChange={() => void onTogglePolicy(policy)}
                        />
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                            onClick={() => openEditDialog(policy)}
                            aria-label="编辑策略"
                          >
                            <Wrench className="size-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                            aria-label={`删除策略 ${policy.name}`}
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
                    <td colSpan={6} className="px-3 py-5">
                      <EmptyState
                        className="py-8"
                        title="暂无匹配策略"
                        description="可调整筛选关键词，或新增策略。"
                        action={(
                          <div className="flex flex-wrap items-center justify-center gap-2">
                            <Button size="sm" variant="outline" onClick={resetFilters}>
                              清空筛选
                            </Button>
                            <Button size="sm" onClick={openCreateDialog}>
                              <Plus className="mr-1 size-4" />
                              新增策略
                            </Button>
                          </div>
                        )}
                      />
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>

      <PolicyEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        editingPolicy={editingPolicy}
        onSave={handleSave}
      />

      {dialog}
    </div>
  );
}
