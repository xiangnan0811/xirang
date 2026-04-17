import { useTranslation } from "react-i18next";
import { Shield } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Input } from "@/components/ui/input";
import type { UserRecord } from "@/types/domain";

type RoleType = UserRecord["role"];

interface UsersTableProps {
  loading: boolean;
  sortedUsers: UserRecord[];
  roleDrafts: Record<number, RoleType>;
  passwordDrafts: Record<number, string>;
  savingUserMap: Record<number, boolean>;
  deletingUserMap: Record<number, boolean>;
  roleOptions: { value: RoleType; label: string }[];
  currentUserId: number | null | undefined;
  onRoleChange: (id: number, role: RoleType) => void;
  onPasswordChange: (id: number, value: string) => void;
  onUpdate: (user: UserRecord) => void;
  onDelete: (user: UserRecord) => void;
}

function TableSkeleton() {
  return (
    <div className="space-y-2">
      {[0, 1, 2].map((i) => (
        <div key={i} className="rounded-xl border border-border/70 p-3">
          <div className="grid gap-2 md:grid-cols-[1fr_160px_1fr_auto] md:items-center">
            <div className="space-y-1.5">
              <Skeleton className="h-4 w-32 rounded" />
              <Skeleton className="h-3 w-24 rounded" />
            </div>
            <Skeleton className="h-9 w-full rounded" />
            <Skeleton className="h-9 w-full rounded" />
            <div className="flex justify-end gap-2">
              <Skeleton className="h-8 w-14 rounded" />
              <Skeleton className="h-8 w-14 rounded" />
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

export function UsersTable({
  loading,
  sortedUsers,
  roleDrafts,
  passwordDrafts,
  savingUserMap,
  deletingUserMap,
  roleOptions,
  currentUserId,
  onRoleChange,
  onPasswordChange,
  onUpdate,
  onDelete,
}: UsersTableProps) {
  const { t } = useTranslation();

  if (loading) {
    return <TableSkeleton />;
  }

  if (sortedUsers.length === 0) {
    return (
      <EmptyState
        title={t("users.emptyTitle")}
        description={t("users.emptyDesc")}
      />
    );
  }

  return (
    <div className="space-y-2">
      {sortedUsers.map((item) => {
        const isSelf = currentUserId === item.id;
        return (
          <div
            key={item.id}
            className="rounded-xl border border-border/70 p-3"
          >
            <div className="grid gap-2 md:grid-cols-[1fr_160px_1fr_auto] md:items-center">
              <div>
                <p className="font-medium">{item.username}</p>
                <p className="text-xs text-muted-foreground">
                  ID: {item.id} · {roleOptions.find((o) => o.value === item.role)?.label ?? item.role}
                  {item.totpEnabled ? (
                    <span className="ml-1.5 inline-flex items-center gap-0.5 rounded bg-success/15 px-1.5 py-0.5 text-[10px] font-medium text-success">
                      <Shield className="size-2.5" aria-hidden="true" />
                      2FA
                    </span>
                  ) : null}
                </p>
              </div>
              <Select
                aria-label={t("users.roleForUser", { username: item.username })}
                value={roleDrafts[item.id] ?? item.role}
                onChange={(e) => onRoleChange(item.id, e.target.value as RoleType)}
                disabled={isSelf}
              >
                {roleOptions.map((one) => (
                  <option key={one.value} value={one.value}>{one.label}</option>
                ))}
              </Select>
              <Input
                type="password"
                aria-label={t("users.passwordForUser", { username: item.username })}
                value={passwordDrafts[item.id] ?? ""}
                onChange={(e) => onPasswordChange(item.id, e.target.value)}
                placeholder={t("users.passwordKeepEmpty")}
              />
              <div className="flex justify-end gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  loading={Boolean(savingUserMap[item.id])}
                  onClick={() => onUpdate(item)}
                >
                  {t("common.save")}
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={isSelf}
                  loading={Boolean(deletingUserMap[item.id])}
                  onClick={() => onDelete(item)}
                >
                  {t("common.delete")}
                </Button>
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}
