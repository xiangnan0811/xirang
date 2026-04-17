import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, LogOut, Shield, Users } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { PageHero } from "@/components/ui/page-hero";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { UserRecord } from "@/types/domain";
import { UsersTable } from "@/pages/users-page.table";
import { CreateUserForm } from "@/pages/users-page.dialogs";

type RoleType = UserRecord["role"];

const roleKeys: RoleType[] = ["admin", "operator", "viewer"];

export function UsersPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();

  const roleOptions = roleKeys.map((key) => ({
    value: key,
    label: t(`users.roles.${key}`),
  }));

  const { confirm, dialog } = useConfirm();
  const { token, username, role, userId, logout } = useAuth();
  const isAdmin = role === "admin";

  const [users, setUsers] = useState<UserRecord[]>([]);
  const [loadingUsers, setLoadingUsers] = useState(false);

  // ── 修改自己密码 ──
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [changingPassword, setChangingPassword] = useState(false);

  // ── 创建用户表单 ──
  const [newUsername, setNewUsername] = useState("");
  const [newUserPassword, setNewUserPassword] = useState("");
  const [newUserRole, setNewUserRole] = useState<RoleType>("operator");
  const [creatingUser, setCreatingUser] = useState(false);

  // ── 用户列表编辑状态 ──
  const [roleDrafts, setRoleDrafts] = useState<Record<number, RoleType>>({});
  const [passwordDrafts, setPasswordDrafts] = useState<Record<number, string>>({});
  const [savingUserMap, setSavingUserMap] = useState<Record<number, boolean>>({});
  const [deletingUserMap, setDeletingUserMap] = useState<Record<number, boolean>>({});

  const loadUsers = useCallback(async () => {
    if (!token || !isAdmin) {
      setUsers([]);
      return;
    }
    setLoadingUsers(true);
    try {
      const rows = await apiClient.getUsers(token);
      setUsers(rows);
      setRoleDrafts(
        Object.fromEntries(rows.map((item) => [item.id, item.role])) as Record<number, RoleType>,
      );
    } catch (error) {
      toast.error(t("users.loadFailed", { error: getErrorMessage(error) }));
    } finally {
      setLoadingUsers(false);
    }
  }, [isAdmin, t, token]);

  useEffect(() => {
    void loadUsers();
  }, [loadUsers]);

  const sortedUsers = useMemo(
    () => [...users].sort((a, b) => a.id - b.id),
    [users],
  );

  const adminCount = useMemo(
    () => users.filter((u) => u.role === "admin").length,
    [users],
  );

  // ── Handlers ──
  const handleChangePassword = async () => {
    if (!token) {
      toast.error(t("users.errorNotLoggedIn"));
      return;
    }
    if (!currentPassword.trim() || !newPassword.trim()) {
      toast.error(t("users.errorPasswordRequired"));
      return;
    }
    if (newPassword !== confirmPassword) {
      toast.error(t("users.errorPasswordMismatch"));
      return;
    }

    setChangingPassword(true);
    try {
      await apiClient.changePassword(token, currentPassword, newPassword);
      await apiClient.logout(token).catch(() => undefined);
      logout();
      toast.success(t("users.passwordChanged"));
      navigate("/login", { replace: true });
    } catch (error) {
      toast.error(t("users.changePasswordFailed", { error: getErrorMessage(error) }));
    } finally {
      setChangingPassword(false);
    }
  };

  const handleCreateUser = async () => {
    if (!token) {
      toast.error(t("users.errorNotLoggedIn"));
      return;
    }
    if (!newUsername.trim() || !newUserPassword.trim()) {
      toast.error(t("users.errorUsernamePasswordRequired"));
      return;
    }
    if (newUserPassword.trim().length < 12) {
      toast.error(t("users.errorPasswordTooShort"));
      return;
    }

    setCreatingUser(true);
    try {
      const created = await apiClient.createUser(token, {
        username: newUsername.trim(),
        password: newUserPassword,
        role: newUserRole,
      });
      setUsers((prev) => [...prev, created]);
      setRoleDrafts((prev) => ({ ...prev, [created.id]: created.role }));
      setPasswordDrafts((prev) => ({ ...prev, [created.id]: "" }));
      setNewUsername("");
      setNewUserPassword("");
      setNewUserRole("operator");
      toast.success(t("users.createSuccess"));
    } catch (error) {
      toast.error(t("users.createFailed", { error: getErrorMessage(error) }));
    } finally {
      setCreatingUser(false);
    }
  };

  const handleUpdateUser = async (target: UserRecord) => {
    if (!token) {
      toast.error(t("users.errorNotLoggedIn"));
      return;
    }
    const roleValue = roleDrafts[target.id] ?? target.role;
    const passwordValue = (passwordDrafts[target.id] ?? "").trim();

    setSavingUserMap((prev) => ({ ...prev, [target.id]: true }));
    try {
      const updated = await apiClient.updateUser(token, target.id, {
        role: roleValue,
        password: passwordValue || undefined,
      });
      setUsers((prev) =>
        prev.map((item) => (item.id === target.id ? updated : item)),
      );
      setRoleDrafts((prev) => ({ ...prev, [target.id]: updated.role }));
      setPasswordDrafts((prev) => ({ ...prev, [target.id]: "" }));
      toast.success(t("users.updateSuccess"));
    } catch (error) {
      toast.error(t("users.updateFailed", { error: getErrorMessage(error) }));
    } finally {
      setSavingUserMap((prev) => ({ ...prev, [target.id]: false }));
    }
  };

  const handleDeleteUser = async (target: UserRecord) => {
    if (!token) {
      toast.error(t("users.errorNotLoggedIn"));
      return;
    }

    const confirmed = await confirm({
      title: t("users.confirmDeleteTitle"),
      description: t("users.confirmDeleteDesc", { username: target.username }),
      confirmText: t("common.delete"),
      cancelText: t("common.cancel"),
    });
    if (!confirmed) return;

    setDeletingUserMap((prev) => ({ ...prev, [target.id]: true }));
    try {
      await apiClient.deleteUser(token, target.id);
      setUsers((prev) => prev.filter((item) => item.id !== target.id));
      toast.success(t("users.deleteSuccess"));
    } catch (error) {
      toast.error(t("users.deleteFailed", { error: getErrorMessage(error) }));
    } finally {
      setDeletingUserMap((prev) => ({ ...prev, [target.id]: false }));
    }
  };

  const handleLogout = async () => {
    if (token) {
      await apiClient.logout(token).catch(() => undefined);
    }
    logout();
    navigate("/login", { replace: true });
  };

  return (
    <div className="animate-fade-in space-y-5">
      {/* ── PageHero ── */}
      <PageHero
        title={t("users.pageTitle")}
        subtitle={
          isAdmin
            ? t("users.pageSubtitle", { count: users.length, admins: adminCount })
            : undefined
        }
      />

      {/* ── 账号安全 ── */}
      <Card>
        <CardContent className="space-y-4 pt-6">
          <div className="flex items-center gap-2 font-medium">
            <KeyRound className="size-4" aria-hidden="true" />
            {t("users.accountSecurity")}
          </div>
          <p className="text-sm text-muted-foreground">
            {t("users.currentLogin", {
              username: username ?? t("common.unknown"),
              role: role ? (roleOptions.find((o) => o.value === role)?.label ?? role) : "",
            })}
          </p>
          <div className="grid gap-3 md:grid-cols-3">
            <Input
              type="password"
              placeholder={t("users.currentPassword")}
              aria-label={t("users.currentPassword")}
              value={currentPassword}
              onChange={(e) => setCurrentPassword(e.target.value)}
            />
            <Input
              type="password"
              placeholder={t("users.newPasswordPlaceholder")}
              aria-label={t("users.newPassword")}
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
            />
            <Input
              type="password"
              placeholder={t("users.confirmPassword")}
              aria-label={t("users.confirmPassword")}
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
            />
          </div>
          <div className="flex flex-wrap gap-2">
            <Button shape="pill" loading={changingPassword} onClick={() => void handleChangePassword()}>
              {t("users.changePassword")}
            </Button>
            <Button shape="pill" variant="outline" onClick={() => void handleLogout()}>
              <LogOut className="mr-2 size-4" aria-hidden="true" />
              {t("appShell.logout")}
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* ── 用户管理（仅管理员） ── */}
      {isAdmin ? (
        <>
          <CreateUserForm
            newUsername={newUsername}
            setNewUsername={setNewUsername}
            newUserPassword={newUserPassword}
            setNewUserPassword={setNewUserPassword}
            newUserRole={newUserRole}
            setNewUserRole={setNewUserRole}
            creating={creatingUser}
            roleOptions={roleOptions}
            onSubmit={() => void handleCreateUser()}
          />

          <Card>
            <CardContent className="space-y-4 pt-6">
              <div className="flex items-center gap-2 font-medium">
                <Users className="size-4" aria-hidden="true" />
                {t("users.userManagement")}
              </div>
              <UsersTable
                loading={loadingUsers}
                sortedUsers={sortedUsers}
                roleDrafts={roleDrafts}
                passwordDrafts={passwordDrafts}
                savingUserMap={savingUserMap}
                deletingUserMap={deletingUserMap}
                roleOptions={roleOptions}
                currentUserId={userId}
                onRoleChange={(id, role) =>
                  setRoleDrafts((prev) => ({ ...prev, [id]: role }))
                }
                onPasswordChange={(id, value) =>
                  setPasswordDrafts((prev) => ({ ...prev, [id]: value }))
                }
                onUpdate={(user) => void handleUpdateUser(user)}
                onDelete={(user) => void handleDeleteUser(user)}
              />
            </CardContent>
          </Card>
        </>
      ) : (
        <Card>
          <CardContent className="pt-6">
            <EmptyState
              title={t("users.insufficientPermission")}
              description={t("users.insufficientPermissionDesc")}
              icon={Shield}
            />
          </CardContent>
        </Card>
      )}

      {dialog}
    </div>
  );
}
