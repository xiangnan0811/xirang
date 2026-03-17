import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, LogOut, Shield, UserPlus, Users } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import { Select } from "@/components/ui/select";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { UserRecord } from "@/types/domain";

type RoleType = UserRecord["role"];

const roleKeys: RoleType[] = ["admin", "operator", "viewer"];

export function UsersPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();

  const roleOptions = roleKeys.map((key) => ({
    value: key,
    label: t(`users.roles.${key}`),
  }));

  const roleLabel = (role: RoleType) =>
    roleOptions.find((item) => item.value === role)?.label ?? role;
  const { confirm, dialog } = useConfirm();
  const { token, username, role, userId, logout } = useAuth();
  const isAdmin = role === "admin";

  const [users, setUsers] = useState<UserRecord[]>([]);
  const [loadingUsers, setLoadingUsers] = useState(false);

  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [changingPassword, setChangingPassword] = useState(false);

  const [newUsername, setNewUsername] = useState("");
  const [newUserPassword, setNewUserPassword] = useState("");
  const [newUserRole, setNewUserRole] = useState<RoleType>("operator");
  const [creatingUser, setCreatingUser] = useState(false);

  const [roleDrafts, setRoleDrafts] = useState<Record<number, RoleType>>({});
  const [passwordDrafts, setPasswordDrafts] = useState<Record<number, string>>(
    {},
  );
  const [savingUserMap, setSavingUserMap] = useState<Record<number, boolean>>(
    {},
  );

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
        Object.fromEntries(rows.map((item) => [item.id, item.role])) as Record<
          number,
          RoleType
        >,
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

  const sortedUsers = useMemo(() => {
    return [...users].sort((a, b) => a.id - b.id);
  }, [users]);

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
      toast.error(
        t("users.changePasswordFailed", { error: getErrorMessage(error) }),
      );
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
      toast.error(
        t("users.createFailed", { error: getErrorMessage(error) }),
      );
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
      toast.error(
        t("users.updateFailed", { error: getErrorMessage(error) }),
      );
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
      description: t("users.confirmDeleteDesc", {
        username: target.username,
      }),
      confirmText: t("common.delete"),
      cancelText: t("common.cancel"),
    });
    if (!confirmed) {
      return;
    }

    setSavingUserMap((prev) => ({ ...prev, [target.id]: true }));
    try {
      await apiClient.deleteUser(token, target.id);
      setUsers((prev) => prev.filter((item) => item.id !== target.id));
      toast.success(t("users.deleteSuccess"));
    } catch (error) {
      toast.error(
        t("users.deleteFailed", { error: getErrorMessage(error) }),
      );
    } finally {
      setSavingUserMap((prev) => ({ ...prev, [target.id]: false }));
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
    <div className="space-y-4">
      <Card>
        <CardContent className="space-y-4 pt-6">
          <div className="flex items-center gap-2 font-medium">
            <KeyRound className="size-4" />
            {t("users.accountSecurity")}
          </div>
          <p className="text-sm text-muted-foreground">
            {t("users.currentLogin", {
              username: username ?? t("common.unknown"),
              role: role ? roleLabel(role) : "",
            })}
          </p>
          <div className="grid gap-3 md:grid-cols-3">
            <Input
              type="password"
              placeholder={t("users.currentPassword")}
              aria-label={t("users.currentPassword")}
              value={currentPassword}
              onChange={(event) => setCurrentPassword(event.target.value)}
            />
            <Input
              type="password"
              placeholder={t("users.newPasswordPlaceholder")}
              aria-label={t("users.newPassword")}
              value={newPassword}
              onChange={(event) => setNewPassword(event.target.value)}
            />
            <Input
              type="password"
              placeholder={t("users.confirmPassword")}
              aria-label={t("users.confirmPassword")}
              value={confirmPassword}
              onChange={(event) => setConfirmPassword(event.target.value)}
            />
          </div>
          <div className="flex flex-wrap gap-2">
            <Button loading={changingPassword} onClick={handleChangePassword}>
              {t("users.changePassword")}
            </Button>
            <Button variant="outline" onClick={handleLogout}>
              <LogOut className="mr-2 size-4" />
              {t("appShell.logout")}
            </Button>
          </div>
        </CardContent>
      </Card>

      {isAdmin ? (
        <Card>
          <CardContent className="space-y-4 pt-6">
            <div className="flex items-center gap-2 font-medium">
              <Users className="size-4" />
              {t("users.userManagement")}
            </div>
            <div className="grid gap-2 md:grid-cols-4">
              <Input
                placeholder={t("users.newUsername")}
                aria-label={t("users.newUsername")}
                value={newUsername}
                onChange={(event) => setNewUsername(event.target.value)}
              />
              <Input
                type="password"
                placeholder={t("users.initialPassword")}
                aria-label={t("users.initialPassword")}
                value={newUserPassword}
                onChange={(event) => setNewUserPassword(event.target.value)}
              />
              <Select
                value={newUserRole}
                onChange={(event) =>
                  setNewUserRole(event.target.value as RoleType)
                }
                options={roleOptions.map((item) => ({
                  value: item.value,
                  label: item.label,
                }))}
              />
              <Button loading={creatingUser} onClick={handleCreateUser}>
                <UserPlus className="mr-2 size-4" />
                {t("users.createUser")}
              </Button>
            </div>

            {loadingUsers ? (
              <LoadingState description={t("users.loadingDesc")} />
            ) : sortedUsers.length === 0 ? (
              <EmptyState
                title={t("users.emptyTitle")}
                description={t("users.emptyDesc")}
              />
            ) : (
              <div className="space-y-2">
                {sortedUsers.map((item) => {
                  const isSelf = userId === item.id;
                  return (
                    <div
                      key={item.id}
                      className="rounded-xl border border-border/70 p-3"
                    >
                      <div className="grid gap-2 md:grid-cols-[1fr_160px_1fr_auto] md:items-center">
                        <div>
                          <p className="font-medium">{item.username}</p>
                          <p className="text-xs text-muted-foreground">
                            ID: {item.id} · {roleLabel(item.role)}
                            {item.totpEnabled ? (
                              <span className="ml-1.5 inline-flex items-center gap-0.5 rounded bg-emerald-500/15 px-1.5 py-0.5 text-[10px] font-medium text-emerald-600 dark:text-emerald-400">
                                <Shield className="size-2.5" />
                                2FA
                              </span>
                            ) : null}
                          </p>
                        </div>
                        <Select
                          value={roleDrafts[item.id] ?? item.role}
                          onChange={(event) =>
                            setRoleDrafts((prev) => ({
                              ...prev,
                              [item.id]: event.target.value as RoleType,
                            }))
                          }
                          options={roleOptions.map((one) => ({
                            value: one.value,
                            label: one.label,
                          }))}
                          disabled={isSelf}
                        />
                        <Input
                          type="password"
                          value={passwordDrafts[item.id] ?? ""}
                          onChange={(event) =>
                            setPasswordDrafts((prev) => ({
                              ...prev,
                              [item.id]: event.target.value,
                            }))
                          }
                          placeholder={t("users.passwordKeepEmpty")}
                        />
                        <div className="flex justify-end gap-2">
                          <Button
                            size="sm"
                            variant="outline"
                            loading={Boolean(savingUserMap[item.id])}
                            onClick={() => void handleUpdateUser(item)}
                          >
                            {t("common.save")}
                          </Button>
                          <Button
                            size="sm"
                            variant="destructive"
                            disabled={isSelf}
                            loading={Boolean(savingUserMap[item.id])}
                            onClick={() => void handleDeleteUser(item)}
                          >
                            {t("common.delete")}
                          </Button>
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </CardContent>
        </Card>
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
