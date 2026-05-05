import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Shield } from "lucide-react";
import { toast } from "@/components/ui/toast";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { useAuth } from "@/context/auth-context";
import {
  createCredentialsApi,
  type AppCredentialInput,
  type AppCredentialResponse,
  type ConfigField,
  type ProfileSchema,
} from "@/lib/api/credentials";
import { getErrorMessage } from "@/lib/utils";

type CredentialEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editingCredential?: AppCredentialResponse | null;
  onSaved: () => void;
};

export function CredentialEditorDialog({
  open,
  onOpenChange,
  editingCredential,
  onSaved,
}: CredentialEditorDialogProps) {
  const { t } = useTranslation();
  const { token } = useAuth();

  const [profiles, setProfiles] = useState<ProfileSchema[]>([]);
  const [loadingProfiles, setLoadingProfiles] = useState(false);
  const [selectedProfileId, setSelectedProfileId] = useState("");
  const [saving, setSaving] = useState(false);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [fieldValues, setFieldValues] = useState<Record<string, string>>({});

  const isEditing = Boolean(editingCredential);

  // Fetch profiles when dialog opens
  useEffect(() => {
    if (!open || !token) return;
    setLoadingProfiles(true);
    createCredentialsApi()
      .listProfiles(token)
      .then((data) => setProfiles(data))
      .catch((err) => toast.error(getErrorMessage(err)))
      .finally(() => setLoadingProfiles(false));
  }, [open, token]);

  // Reset form when dialog opens or editing credential changes
  useEffect(() => {
    if (!open) return;
    if (editingCredential) {
      setName(editingCredential.name);
      setDescription(editingCredential.description ?? "");
      setSelectedProfileId(editingCredential.type);
      const values: Record<string, string> = {};
      if (editingCredential.config) {
        for (const [k, v] of Object.entries(editingCredential.config)) {
          values[k] = v ?? "";
        }
      }
      setFieldValues(values);
    } else {
      setName("");
      setDescription("");
      setSelectedProfileId("");
      setFieldValues({});
    }
  }, [open, editingCredential]);

  const selectedProfile = useMemo(
    () => profiles.find((p) => p.credential_type === selectedProfileId) ?? null,
    [profiles, selectedProfileId],
  );

  const profileOptions = useMemo(() => {
    const nonDocker = profiles.filter((p) => !p.is_docker);
    const docker = profiles.filter((p) => p.is_docker);
    return { nonDocker, docker };
  }, [profiles]);

  const handleProfileChange = (value: string) => {
    setSelectedProfileId(value);
    if (!isEditing) {
      setFieldValues({});
    }
  };

  const handleFieldChange = (key: string, value: string) => {
    setFieldValues((prev) => ({ ...prev, [key]: value }));
  };

  const handleSubmit = async () => {
    if (!token) return;
    if (!name.trim()) {
      toast.error(t("credentials.validation.nameRequired"));
      return;
    }
    if (!selectedProfileId) {
      toast.error(t("credentials.validation.typeRequired"));
      return;
    }

    const profile = selectedProfile;
    if (!profile) {
      toast.error(t("credentials.validation.invalidProfile"));
      return;
    }

    // Validate required fields
    for (const field of profile.config_schema) {
      if (field.required && !fieldValues[field.key]?.trim()) {
        // For password on edit, allow empty (means preserve)
        if (field.type === "password" && isEditing) continue;
        toast.error(
          t("credentials.validation.fieldRequired", { field: field.label }),
        );
        return;
      }
    }

    // Validate container_name for docker types
    if (profile.is_docker && !fieldValues.container_name?.trim()) {
      toast.error(t("credentials.validation.containerNameRequired"));
      return;
    }

    setSaving(true);
    try {
      const api = createCredentialsApi();
      const input: AppCredentialInput = {
        type: selectedProfileId,
        name: name.trim(),
        description: description.trim() || undefined,
        host: fieldValues.host?.trim() || undefined,
        port: fieldValues.port?.trim() || undefined,
        user: fieldValues.user?.trim() || undefined,
        password: fieldValues.password?.trim() || undefined,
        container_name: profile.is_docker
          ? fieldValues.container_name?.trim() || undefined
          : undefined,
      };

      if (isEditing && editingCredential) {
        await api.update(token, editingCredential.id, input);
        toast.success(t("credentials.updated"));
      } else {
        await api.create(token, input);
        toast.success(t("credentials.created"));
      }

      onSaved();
      onOpenChange(false);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setSaving(false);
    }
  };

  const renderField = (field: ConfigField) => (
    <div key={field.key}>
      <label
        htmlFor={`credential-field-${field.key}`}
        className="mb-1 block text-sm font-medium"
      >
        {field.label}
        {field.required ? (
          <span className="ml-0.5 text-destructive" aria-hidden="true">
            *
          </span>
        ) : null}
      </label>
      <Input
        id={`credential-field-${field.key}`}
        type={field.type === "number" ? "number" : "text"}
        placeholder={field.placeholder ?? ""}
        value={fieldValues[field.key] ?? ""}
        onChange={(event) =>
          handleFieldChange(field.key, event.target.value)
        }
      />
    </div>
  );

  const renderPasswordField = (field: ConfigField) => (
    <div key={field.key}>
      <label
        htmlFor={`credential-field-${field.key}`}
        className="mb-1 block text-sm font-medium"
      >
        {field.label}
        {field.required ? (
          <span className="ml-0.5 text-destructive" aria-hidden="true">
            *
          </span>
        ) : null}
      </label>
      <Input
        id={`credential-field-${field.key}`}
        type="password"
        placeholder={
          isEditing
            ? t("credentials.leaveEmptyToKeep")
            : (field.placeholder ?? "")
        }
        value={fieldValues[field.key] ?? ""}
        onChange={(event) =>
          handleFieldChange(field.key, event.target.value)
        }
      />
      {isEditing && (
        <p className="mt-1 text-xs text-muted-foreground">
          {t("credentials.leaveEmptyToKeep")}
        </p>
      )}
    </div>
  );

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      size="sm"
      icon={<Shield className="size-5 text-primary" />}
      title={
        isEditing ? t("credentials.editTitle") : t("credentials.createTitle")
      }
      description={
        isEditing ? t("credentials.editDesc") : t("credentials.createDesc")
      }
      saving={saving}
      onSubmit={handleSubmit}
      submitLabel={isEditing ? t("common.save") : t("common.create")}
    >
      {/* Name */}
      <div>
        <label
          htmlFor="credential-name"
          className="mb-1 block text-sm font-medium"
        >
          {t("common.name")}
          <span className="ml-0.5 text-destructive" aria-hidden="true">
            *
          </span>
        </label>
        <Input
          id="credential-name"
          placeholder={t("credentials.namePlaceholder")}
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </div>

      {/* Description */}
      <div>
        <label
          htmlFor="credential-description"
          className="mb-1 block text-sm font-medium"
        >
          {t("common.description")}
        </label>
        <Input
          id="credential-description"
          placeholder={t("credentials.descPlaceholder")}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />
      </div>

      {/* Profile type selector */}
      <div>
        <label
          htmlFor="credential-type"
          className="mb-1 block text-sm font-medium"
        >
          {t("common.type")}
          <span className="ml-0.5 text-destructive" aria-hidden="true">
            *
          </span>
        </label>
        <Select
          id="credential-type"
          containerClassName="w-full"
          value={selectedProfileId}
          onChange={(event) => handleProfileChange(event.target.value)}
          disabled={loadingProfiles || isEditing}
        >
          <option value="">{t("credentials.selectType")}</option>
          {profileOptions.nonDocker.length > 0 && (
            <optgroup label={t("credentials.groupNonDocker")}>
              {profileOptions.nonDocker.map((p) => (
                <option key={p.id} value={p.credential_type}>
                  {p.name}
                </option>
              ))}
            </optgroup>
          )}
          {profileOptions.docker.length > 0 && (
            <optgroup label={t("credentials.groupDocker")}>
              {profileOptions.docker.map((p) => (
                <option key={p.id} value={p.credential_type}>
                  {p.name}
                </option>
              ))}
            </optgroup>
          )}
        </Select>
        {selectedProfile?.description && (
          <p className="mt-1 text-xs text-muted-foreground">
            {selectedProfile.description}
          </p>
        )}
      </div>

      {/* Dynamic config fields */}
      {selectedProfile?.config_schema?.map((field) =>
        field.type === "password"
          ? renderPasswordField(field)
          : renderField(field),
      )}
    </FormDialog>
  );
}
