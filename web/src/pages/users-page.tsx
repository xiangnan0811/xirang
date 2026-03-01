import { useCallback, useEffect, useMemo, useState } from "react";
import { KeyRound, LogOut, Shield, UserPlus, Users } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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

const roleOptions: Array<{ value: RoleType; label: string }> = [
  { value: "admin", label: "管理员" },
  { value: "operator", label: "运维" },
  { value: "viewer", label: "只读" }
];

function roleLabel(role: RoleType) {
  return roleOptions.find((item) => item.value === role)?.label ?? role;
}

export function UsersPage() {
  const navigate = useNavigate();
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
  const [passwordDrafts, setPasswordDrafts] = useState<Record<number, string>>({});
  const [savingUserMap, setSavingUserMap] = useState<Record<number, boolean>>({});

  const loadUsers = useCallback(async () => {
    if (!token || !isAdmin) {
      setUsers([]);
      return;
    }
    setLoadingUsers(true);
    try {
      const rows = await apiClient.getUsers(token);
      setUsers(rows);
      setRoleDrafts(Object.fromEntries(rows.map((item) => [item.id, item.role])) as Record<number, RoleType>);
    } catch (error) {
      toast.error(`加载用户失败：${getErrorMessage(error)}`);
    } finally {
      setLoadingUsers(false);
    }
  }, [isAdmin, token]);

  useEffect(() => {
    void loadUsers();
  }, [loadUsers]);

  const sortedUsers = useMemo(() => {
    return [...users].sort((a, b) => a.id - b.id);
  }, [users]);

  const handleChangePassword = async () => {
    if (!token) {
      toast.error("当前未登录，请重新登录后重试。");
      return;
    }
    if (!currentPassword.trim() || !newPassword.trim()) {
      toast.error("请填写当前密码和新密码。");
      return;
    }
    if (newPassword !== confirmPassword) {
      toast.error("两次输入的新密码不一致。");
      return;
    }

    setChangingPassword(true);
    try {
      await apiClient.changePassword(token, currentPassword, newPassword);
      await apiClient.logout(token).catch(() => undefined);
      logout();
      toast.success("密码修改成功，请重新登录。");
      navigate("/login", { replace: true });
    } catch (error) {
      toast.error(`修改密码失败：${getErrorMessage(error)}`);
    } finally {
      setChangingPassword(false);
    }
  };

  const handleCreateUser = async () => {
    if (!token) {
      toast.error("当前未登录，请重新登录后重试。");
      return;
    }
    if (!newUsername.trim() || !newUserPassword.trim()) {
      toast.error("用户名和初始密码不能为空。");
      return;
    }
    if (newUserPassword.trim().length < 12) {
      toast.error("初始密码至少需要 12 位，且包含大小写字母、数字和符号。");
      return;
    }

    setCreatingUser(true);
    try {
      const created = await apiClient.createUser(token, {
        username: newUsername.trim(),
        password: newUserPassword,
        role: newUserRole
      });
      setUsers((prev) => [...prev, created]);
      setRoleDrafts((prev) => ({ ...prev, [created.id]: created.role }));
      setPasswordDrafts((prev) => ({ ...prev, [created.id]: "" }));
      setNewUsername("");
      setNewUserPassword("");
      setNewUserRole("operator");
      toast.success("用户创建成功");
    } catch (error) {
      toast.error(`创建用户失败：${getErrorMessage(error)}`);
    } finally {
      setCreatingUser(false);
    }
  };

  const handleUpdateUser = async (target: UserRecord) => {
    if (!token) {
      toast.error("当前未登录，请重新登录后重试。");
      return;
    }
    const roleValue = roleDrafts[target.id] ?? target.role;
    const passwordValue = (passwordDrafts[target.id] ?? "").trim();

    setSavingUserMap((prev) => ({ ...prev, [target.id]: true }));
    try {
      const updated = await apiClient.updateUser(token, target.id, {
        role: roleValue,
        password: passwordValue || undefined
      });
      setUsers((prev) => prev.map((item) => (item.id === target.id ? updated : item)));
      setRoleDrafts((prev) => ({ ...prev, [target.id]: updated.role }));
      setPasswordDrafts((prev) => ({ ...prev, [target.id]: "" }));
      toast.success("用户更新成功");
    } catch (error) {
      toast.error(`更新用户失败：${getErrorMessage(error)}`);
    } finally {
      setSavingUserMap((prev) => ({ ...prev, [target.id]: false }));
    }
  };

  const handleDeleteUser = async (target: UserRecord) => {
    if (!token) {
      toast.error("当前未登录，请重新登录后重试。");
      return;
    }

    const confirmed = await confirm({
      title: "确认删除用户",
      description: `删除后将无法恢复：${target.username}`,
      confirmText: "删除",
      cancelText: "取消"
    });
    if (!confirmed) {
      return;
    }

    setSavingUserMap((prev) => ({ ...prev, [target.id]: true }));
    try {
      await apiClient.deleteUser(token, target.id);
      setUsers((prev) => prev.filter((item) => item.id !== target.id));
      toast.success("用户删除成功");
    } catch (error) {
      toast.error(`删除用户失败：${getErrorMessage(error)}`);
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
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <KeyRound className="size-4" />
            账号安全
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">
            当前登录：{username ?? "未知"} {role ? `(${roleLabel(role)})` : ""}
          </p>
          <div className="grid gap-3 md:grid-cols-3">
            <Input
  type="password"
  placeholder="当前密码"
  aria-label="当前密码"
  value={currentPassword}
  onChange={(event) => setCurrentPassword(event.target.value)}
/>
            <Input
  type="password"
  placeholder="新密码（至少12位，含大小写/数字/符号）"
  aria-label="新密码"
  value={newPassword}
  onChange={(event) => setNewPassword(event.target.value)}
/>
            <Input
  type="password"
  placeholder="确认新密码"
  aria-label="确认新密码"
  value={confirmPassword}
  onChange={(event) => setConfirmPassword(event.target.value)}
/>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button loading={changingPassword} onClick={handleChangePassword}>
              修改密码
            </Button>
            <Button variant="outline" onClick={handleLogout}>
              <LogOut className="mr-2 size-4" />
              退出登录
            </Button>
          </div>
        </CardContent>
      </Card>

      {isAdmin ? (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Users className="size-4" />
              用户管理
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-2 md:grid-cols-4">
              <Input
  placeholder="新用户名"
  aria-label="新用户名"
  value={newUsername}
  onChange={(event) => setNewUsername(event.target.value)}
/>
              <Input
  type="password"
  placeholder="初始密码"
  aria-label="初始密码"
  value={newUserPassword}
  onChange={(event) => setNewUserPassword(event.target.value)}
/>
              <Select
                value={newUserRole}
                onChange={(event) => setNewUserRole(event.target.value as RoleType)}
                options={roleOptions.map((item) => ({ value: item.value, label: item.label }))}
              />
              <Button loading={creatingUser} onClick={handleCreateUser}>
                <UserPlus className="mr-2 size-4" />
                创建用户
              </Button>
            </div>

            {loadingUsers ? (
              <LoadingState description="正在加载用户列表..." />
            ) : sortedUsers.length === 0 ? (
              <EmptyState title="暂无用户" description="请先创建用户账号" />
            ) : (
              <div className="space-y-2">
                {sortedUsers.map((item) => {
                  const isSelf = userId === item.id;
                  return (
                    <div key={item.id} className="rounded-xl border border-border/70 p-3">
                      <div className="grid gap-2 md:grid-cols-[1fr_160px_1fr_auto] md:items-center">
                        <div>
                          <p className="font-medium">{item.username}</p>
                          <p className="text-xs text-muted-foreground">ID: {item.id} · {roleLabel(item.role)}</p>
                        </div>
                        <Select
                          value={roleDrafts[item.id] ?? item.role}
                          onChange={(event) =>
                            setRoleDrafts((prev) => ({ ...prev, [item.id]: event.target.value as RoleType }))
                          }
                          options={roleOptions.map((one) => ({ value: one.value, label: one.label }))}
                          disabled={isSelf}
                        />
                        <Input
                          type="password"
                          value={passwordDrafts[item.id] ?? ""}
                          onChange={(event) =>
                            setPasswordDrafts((prev) => ({ ...prev, [item.id]: event.target.value }))
                          }
                          placeholder="留空则不改密码"
                        />
                        <div className="flex gap-2 justify-end">
                          <Button
                            size="sm"
                            variant="outline"
                            loading={Boolean(savingUserMap[item.id])}
                            onClick={() => void handleUpdateUser(item)}
                          >
                            保存
                          </Button>
                          <Button
                            size="sm"
                            variant="destructive"
                            disabled={isSelf}
                            loading={Boolean(savingUserMap[item.id])}
                            onClick={() => void handleDeleteUser(item)}
                          >
                            删除
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
              title="权限不足"
              description="当前角色仅可修改自己的密码，不具备用户管理权限。"
              icon={Shield}
            />
          </CardContent>
        </Card>
      )}

      {dialog}
    </div>
  );
}
