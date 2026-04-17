import { useTranslation } from "react-i18next";
import { UserPlus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Card, CardContent } from "@/components/ui/card";
import type { UserRecord } from "@/types/domain";

type RoleType = UserRecord["role"];

interface CreateUserFormProps {
  newUsername: string;
  setNewUsername: (v: string) => void;
  newUserPassword: string;
  setNewUserPassword: (v: string) => void;
  newUserRole: RoleType;
  setNewUserRole: (v: RoleType) => void;
  creating: boolean;
  roleOptions: { value: RoleType; label: string }[];
  onSubmit: () => void;
}

export function CreateUserForm({
  newUsername,
  setNewUsername,
  newUserPassword,
  setNewUserPassword,
  newUserRole,
  setNewUserRole,
  creating,
  roleOptions,
  onSubmit,
}: CreateUserFormProps) {
  const { t } = useTranslation();

  return (
    <Card>
      <CardContent className="space-y-4 pt-6">
        <div className="text-sm font-medium">{t("users.createUserTitle")}</div>
        <div className="grid gap-2 md:grid-cols-4">
          <Input
            placeholder={t("users.newUsername")}
            aria-label={t("users.newUsername")}
            value={newUsername}
            onChange={(e) => setNewUsername(e.target.value)}
          />
          <Input
            type="password"
            placeholder={t("users.initialPassword")}
            aria-label={t("users.initialPassword")}
            value={newUserPassword}
            onChange={(e) => setNewUserPassword(e.target.value)}
          />
          <Select
            value={newUserRole}
            onChange={(e) => setNewUserRole(e.target.value as RoleType)}
          >
            {roleOptions.map((item) => (
              <option key={item.value} value={item.value}>{item.label}</option>
            ))}
          </Select>
          <Button loading={creating} onClick={onSubmit}>
            <UserPlus className="mr-2 size-4" aria-hidden="true" />
            {t("users.createUser")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
