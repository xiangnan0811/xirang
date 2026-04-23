import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { MoreVertical, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useAuth } from "@/context/auth-context";
import {
  listDashboards,
  createDashboard,
  deleteDashboard,
  type DashboardInput,
} from "@/lib/api/dashboards";
import type { Dashboard, DashboardTimeRange } from "@/types/domain";
import { getErrorMessage } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select } from "@/components/ui/select";

// ─── 删除确认对话框 ───────────────────────────────────────────────

type DeleteDialogProps = {
  dashboard: Dashboard | null;
  onClose: () => void;
  onConfirm: (id: number) => Promise<void>;
};

function DeleteDialog({ dashboard, onClose, onConfirm }: DeleteDialogProps) {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);

  async function handleConfirm() {
    if (!dashboard) return;
    setLoading(true);
    try {
      await onConfirm(dashboard.id);
      onClose();
    } finally {
      setLoading(false);
    }
  }

  return (
    <Dialog open={!!dashboard} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("common.confirmAction")}</DialogTitle>
          <DialogDescription className="sr-only">
            {dashboard
              ? t("dashboards.deleteConfirm", { name: dashboard.name })
              : t("common.confirmAction")}
          </DialogDescription>
        </DialogHeader>
        <p className="px-6 py-3 text-sm text-muted-foreground">
          {dashboard
            ? t("dashboards.deleteConfirm", { name: dashboard.name })
            : ""}
        </p>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={loading}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="destructive"
            onClick={handleConfirm}
            disabled={loading}
          >
            {t("common.delete")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ─── 新建看板对话框 ───────────────────────────────────────────────

type CreateDialogProps = {
  open: boolean;
  onClose: () => void;
  onCreated: (d: Dashboard) => void;
};

const TIME_RANGE_OPTIONS: { value: DashboardTimeRange; labelKey: string }[] = [
  { value: "1h", labelKey: "dashboards.timeRange.1h" },
  { value: "6h", labelKey: "dashboards.timeRange.6h" },
  { value: "24h", labelKey: "dashboards.timeRange.24h" },
  { value: "7d", labelKey: "dashboards.timeRange.7d" },
];

const AUTO_REFRESH_OPTIONS = [
  { value: "0", labelKey: "dashboards.autoRefresh.off" },
  { value: "10", labelKey: "dashboards.autoRefresh.10" },
  { value: "30", labelKey: "dashboards.autoRefresh.30" },
  { value: "60", labelKey: "dashboards.autoRefresh.60" },
  { value: "300", labelKey: "dashboards.autoRefresh.300" },
];

function CreateDialog({ open, onClose, onCreated }: CreateDialogProps) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [timeRange, setTimeRange] = useState<DashboardTimeRange>("1h");
  const [autoRefresh, setAutoRefresh] = useState("30");
  const [nameError, setNameError] = useState("");
  const [loading, setLoading] = useState(false);

  function reset() {
    setName("");
    setDescription("");
    setTimeRange("1h");
    setAutoRefresh("30");
    setNameError("");
    setLoading(false);
  }

  function handleClose() {
    reset();
    onClose();
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed || trimmed.length > 100) {
      setNameError(!trimmed ? t("dashboards.fields.name") : t("validation.maxLength", { max: 100 }));
      return;
    }
    setNameError("");
    setLoading(true);
    try {
      const input: DashboardInput = {
        name: trimmed,
        description: description.trim(),
        time_range: timeRange,
        auto_refresh_seconds: parseInt(autoRefresh, 10),
      };
      const created = await createDashboard(token ?? "", input);
      toast.success(t("common.success"));
      onCreated(created);
      reset();
    } catch (err) {
      const msg = getErrorMessage(err);
      if (msg.includes("409") || msg.toLowerCase().includes("duplicate") || msg.toLowerCase().includes("conflict")) {
        setNameError(t("dashboards.fields.name") + t("common.operationFailed"));
      } else {
        toast.error(msg);
      }
    } finally {
      setLoading(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("dashboards.newButton")}</DialogTitle>
          <DialogDescription className="sr-only">
            {t("dashboards.newButton")}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="px-6 pb-0 pt-3 space-y-4">
          {/* 名称 */}
          <div className="space-y-1.5">
            <label className="text-sm font-medium">
              {t("dashboards.fields.name")}
              <span className="ml-0.5 text-destructive">*</span>
            </label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={100}
              disabled={loading}
              placeholder={t("dashboards.fields.name")}
            />
            {nameError && (
              <p className="text-xs text-destructive">{nameError}</p>
            )}
          </div>

          {/* 描述 */}
          <div className="space-y-1.5">
            <label className="text-sm font-medium">
              {t("dashboards.fields.description")}
            </label>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              disabled={loading}
              placeholder={t("dashboards.fields.description")}
            />
          </div>

          {/* 时间范围 */}
          <div className="space-y-1.5">
            <label className="text-sm font-medium">
              {t("dashboards.fields.timeRange")}
            </label>
            <Select
              value={timeRange}
              onChange={(e) => setTimeRange(e.target.value as DashboardTimeRange)}
              disabled={loading}
            >
              {TIME_RANGE_OPTIONS.map((r) => (
                <option key={r.value} value={r.value}>
                  {t(r.labelKey)}
                </option>
              ))}
            </Select>
          </div>

          {/* 自动刷新 */}
          <div className="space-y-1.5">
            <label className="text-sm font-medium">
              {t("dashboards.fields.autoRefresh")}
            </label>
            <Select
              value={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.value)}
              disabled={loading}
            >
              {AUTO_REFRESH_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {t(o.labelKey)}
                </option>
              ))}
            </Select>
          </div>

          <DialogFooter className="mt-4">
            <Button
              type="button"
              variant="outline"
              onClick={handleClose}
              disabled={loading}
            >
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={loading}>
              {t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ─── 骨架加载 ────────────────────────────────────────────────────

function DashboardCardSkeleton() {
  return (
    <Card>
      <CardHeader>
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-3 w-24 mt-1" />
      </CardHeader>
      <CardContent>
        <Skeleton className="h-3 w-full" />
        <Skeleton className="h-3 w-2/3 mt-2" />
      </CardContent>
    </Card>
  );
}

// ─── 主页面 ──────────────────────────────────────────────────────

export function DashboardsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const navigate = useNavigate();

  const [dashboards, setDashboards] = useState<Dashboard[]>([]);
  const [loading, setLoading] = useState(true);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Dashboard | null>(null);

  useEffect(() => {
    let cancelled = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);
    listDashboards(token ?? "")
      .then((list) => {
        if (!cancelled) setDashboards(list);
      })
      .catch((err: unknown) => {
        if (!cancelled) toast.error(getErrorMessage(err));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  function handleCreated(d: Dashboard) {
    setCreateOpen(false);
    navigate(`/app/dashboards/${d.id}`);
  }

  async function handleDelete(id: number) {
    await deleteDashboard(token ?? "", id);
    setDashboards((prev) => prev.filter((d) => d.id !== id));
    toast.success(t("common.success"));
  }

  return (
    <div className="flex flex-col gap-6 p-6">
      {/* 页头 */}
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t("dashboards.pageTitle")}</h1>
        <Button onClick={() => setCreateOpen(true)}>
          {t("dashboards.newButton")}
        </Button>
      </div>

      {/* 内容区 */}
      {loading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <DashboardCardSkeleton />
          <DashboardCardSkeleton />
          <DashboardCardSkeleton />
        </div>
      ) : dashboards.length === 0 ? (
        /* 空态 */
        <div className="flex flex-col items-center justify-center gap-4 py-24 text-center">
          <p className="text-lg font-medium">{t("dashboards.empty.title")}</p>
          <p className="text-sm text-muted-foreground">
            {t("dashboards.empty.hint")}
          </p>
          <Button onClick={() => setCreateOpen(true)}>
            {t("dashboards.newButton")}
          </Button>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {dashboards.map((d) => (
            <Card
              key={d.id}
              className="relative cursor-pointer transition-shadow hover:shadow-md"
              onClick={() => navigate(`/app/dashboards/${d.id}`)}
            >
              {/* 下拉操作菜单 — 阻止冒泡避免触发卡片点击 */}
              <div
                className="absolute right-3 top-3"
                onClick={(e) => e.stopPropagation()}
              >
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-7"
                      aria-label={t("common.more")}
                    >
                      <MoreVertical className="size-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem
                      className="text-destructive focus:text-destructive"
                      onClick={() => setDeleteTarget(d)}
                    >
                      <Trash2 className="mr-2 size-4" />
                      {t("common.delete")}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>

              <CardHeader className="pr-10">
                <p className="font-medium leading-tight">{d.name}</p>
                <p className="text-xs text-muted-foreground">
                  {t(`dashboards.timeRange.${d.time_range}`)}
                </p>
              </CardHeader>
              <CardContent>
                {d.description && (
                  <p className="line-clamp-2 text-sm text-muted-foreground">
                    {d.description}
                  </p>
                )}
                <p className="mt-2 text-xs text-muted-foreground">
                  {new Date(d.updated_at).toLocaleString()}
                </p>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {/* 新建对话框 */}
      <CreateDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={handleCreated}
      />

      {/* 删除确认对话框 */}
      <DeleteDialog
        dashboard={deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={handleDelete}
      />
    </div>
  );
}
