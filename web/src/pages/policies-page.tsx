import { useMemo, useState } from "react";
import { Clock3, LayoutGrid, List, Plus, Trash2 } from "lucide-react";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import {
  PolicyEditorDialog,
  type PolicyDraft,
} from "@/components/policy-editor-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { usePersistentState } from "@/hooks/use-persistent-state";
import type { NewPolicyInput, PolicyRecord } from "@/types/domain";

const keywordStorageKey = "xirang.policies.keyword";
const viewStorageKey = "xirang.policies.view";

type PoliciesViewMode = "cards" | "list";

export function PoliciesPage() {
  const {
    policies,
    loading,
    globalSearch,
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
  } = useOutletContext<ConsoleOutletContext>();

  const [keyword, setKeyword] = usePersistentState<string>(keywordStorageKey, "");
  const [viewModeRaw, setViewModeRaw] = usePersistentState<string>(viewStorageKey, "cards");

  const viewMode: PoliciesViewMode = viewModeRaw === "list" ? "list" : "cards";

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingPolicy, setEditingPolicy] = useState<PolicyRecord | null>(null);
  const { confirm, dialog } = useConfirm();

  const filteredPolicies = useMemo(() => {
    const effectiveKeyword = (keyword || globalSearch).trim().toLowerCase();
    if (!effectiveKeyword) {
      return policies;
    }
    return policies.filter((policy) =>
      `${policy.name} ${policy.sourcePath} ${policy.targetPath} ${policy.cron}`
        .toLowerCase()
        .includes(effectiveKeyword)
    );
  }, [globalSearch, keyword, policies]);

  const activeCount = policies.filter((policy) => policy.enabled).length;
  const disabledCount = policies.length - activeCount;

  const resetFilters = () => {
    setKeyword("");
  };

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
      toast.error((error as Error).message);
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
      toast.error((error as Error).message);
    }
  };

  const onTogglePolicy = async (policy: PolicyRecord) => {
    try {
      await togglePolicy(policy.id);
      toast.success(`策略 ${policy.name} 已${policy.enabled ? "停用" : "启用"}。`);
    } catch (error) {
      toast.error((error as Error).message);
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="border-border/75">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base">策略配置中枢（平板双栏 + 视图持久化）</CardTitle>
              <p className="mt-1 text-xs text-muted-foreground">卡片视图适合管理，列表视图适合批量审阅</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="success">启用 {activeCount}</Badge>
              <Badge variant="outline">停用 {disabledCount}</Badge>
              <Badge variant="secondary" className="hidden lg:inline-flex">筛选 {filteredPolicies.length}</Badge>
              <div className="inline-flex items-center gap-1 rounded-lg border border-border/80 bg-background/70 p-1">
                <Button
                  size="sm"
                  variant={viewMode === "cards" ? "default" : "ghost"}
                  onClick={() => setViewModeRaw("cards")}
                >
                  <LayoutGrid className="mr-1 size-4" />
                  卡片
                </Button>
                <Button
                  size="sm"
                  variant={viewMode === "list" ? "default" : "ghost"}
                  onClick={() => setViewModeRaw("list")}
                >
                  <List className="mr-1 size-4" />
                  列表
                </Button>
              </div>
              <Button size="sm" onClick={openCreateDialog}>
                <Plus className="mr-1 size-4" />
                新增策略
              </Button>
            </div>
          </div>
        </CardHeader>

        <CardContent className="space-y-3">
          <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-[1fr_auto] lg:grid-cols-[1fr_auto_auto]">
            <Input
              placeholder="搜索策略 / 路径 / cron"
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

          {viewMode === "cards" ? (
            <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3">
              {filteredPolicies.map((policy) => (
                <div
                  key={policy.id}
                  className="interactive-surface p-4"
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

                  <div className="mt-3 grid gap-2 text-sm text-muted-foreground">
                    <p className="break-all">源路径：{policy.sourcePath}</p>
                    <p className="break-all">目标路径：{policy.targetPath}</p>
                  </div>

                  <div className="mt-3 flex flex-wrap gap-2">
                    <Badge variant="outline">Cron: {policy.cron}</Badge>
                    <Badge variant="outline">失败阈值: {policy.criticalThreshold}</Badge>
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
          ) : (
            <div className="overflow-x-auto rounded-2xl border border-border/70 bg-background/55 shadow-sm">
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
                      <tr key={policy.id} className="border-b border-border/60 transition-colors hover:bg-accent/35">
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
                            onCheckedChange={() => void onTogglePolicy(policy)}
                          />
                        </td>
                        <td className="px-3 py-2.5 text-right">
                          <div className="flex justify-end gap-2">
                            <Button size="sm" variant="outline" onClick={() => openEditDialog(policy)}>
                              <Clock3 className="mr-1 size-4" />
                              编辑
                            </Button>
                            <Button
                              size="sm"
                              variant="danger"
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
          )}
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
