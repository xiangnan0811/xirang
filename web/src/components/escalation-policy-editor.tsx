import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogBody,
  DialogFooter,
  DialogCloseButton,
} from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast";
import { EscalationLevelRow, type EscalationLevelRowErrors } from "./escalation-level-row";
import { createIntegrationsApi } from "@/lib/api/integrations-api";
import {
  createEscalationPolicy,
  updateEscalationPolicy,
  type EscalationPolicyInput,
} from "@/lib/api/escalation";
import { getErrorMessage } from "@/lib/utils";
import { useAuth } from "@/context/auth-context";
import type { EscalationLevel, EscalationPolicy, IntegrationChannel } from "@/types/domain";

const intApi = createIntegrationsApi();

const MAX_LEVELS = 5;

const defaultLevel = (): EscalationLevel => ({
  delay_seconds: 0,
  integration_ids: [],
  severity_override: "",
  tags: [],
});

type LevelErrors = EscalationLevelRowErrors;

type FormErrors = {
  name?: string;
  levels?: LevelErrors[];
};

function validateForm(
  name: string,
  levels: EscalationLevel[],
  t: (key: string) => string
): FormErrors {
  const errors: FormErrors = {};

  if (!name.trim()) {
    errors.name = t("escalation.validation.nameRequired");
  } else if (name.length > 100) {
    errors.name = t("escalation.validation.nameTooLong");
  }

  const levelErrors: LevelErrors[] = levels.map((lv, i) => {
    const le: LevelErrors = {};
    if (i === 0 && lv.delay_seconds !== 0) {
      le.delay = t("escalation.validation.firstDelayMustBeZero");
    }
    if (i > 0 && lv.delay_seconds <= levels[i - 1].delay_seconds) {
      le.delay = t("escalation.validation.delayMustIncrease");
    }
    if (lv.integration_ids.length === 0) {
      le.integrations = t("escalation.validation.integrationsRequired");
    }
    const longTag = lv.tags.find((tag) => tag.length > 32);
    if (longTag !== undefined) {
      le.tags = t("escalation.validation.tagTooLong");
    } else if (lv.tags.length > 10) {
      le.tags = t("escalation.validation.tooManyTags");
    }
    return le;
  });

  if (levelErrors.some((le) => Object.keys(le).length > 0)) {
    errors.levels = levelErrors;
  }

  return errors;
}

function hasErrors(errors: FormErrors): boolean {
  if (errors.name) return true;
  if (errors.levels?.some((le) => Object.keys(le).length > 0)) return true;
  return false;
}

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  policy?: EscalationPolicy;
  onSaved: (policy: EscalationPolicy) => void;
};

export function EscalationPolicyEditor({ open, onOpenChange, policy, onSaved }: Props) {
  const { t } = useTranslation();
  const { token } = useAuth();

  const isEdit = Boolean(policy);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [minSeverity, setMinSeverity] = useState<"info" | "warning" | "critical">("warning");
  const [enabled, setEnabled] = useState(true);
  const [levels, setLevels] = useState<EscalationLevel[]>([defaultLevel()]);
  const [integrations, setIntegrations] = useState<IntegrationChannel[]>([]);
  const [saving, setSaving] = useState(false);
  const [nameConflict, setNameConflict] = useState(false);

  // Compute errors on every render (controlled validation)
  const errors = validateForm(name, levels, t);
  const isValid = !hasErrors(errors) && !nameConflict;

  // Reset & load when dialog opens
  useEffect(() => {
    if (!open) return;

    if (policy) {
      setName(policy.name);
      setDescription(policy.description ?? "");
      setMinSeverity(policy.min_severity);
      setEnabled(policy.enabled);
      setLevels(policy.levels.length > 0 ? policy.levels : [defaultLevel()]);
    } else {
      setName("");
      setDescription("");
      setMinSeverity("warning");
      setEnabled(true);
      setLevels([defaultLevel()]);
    }
    setNameConflict(false);
    setSaving(false);

    // Fetch integrations
    if (token) {
      intApi.getIntegrations(token).then(setIntegrations).catch(() => {
        /* ignore — gracefully degraded */
      });
    }
  }, [open, policy, token]);

  const updateLevel = (index: number, next: EscalationLevel) => {
    setLevels((prev) => prev.map((lv, i) => (i === index ? next : lv)));
  };

  const addLevel = () => {
    if (levels.length >= MAX_LEVELS) return;
    const prevDelay = levels[levels.length - 1]?.delay_seconds ?? 0;
    setLevels((prev) => [
      ...prev,
      { ...defaultLevel(), delay_seconds: prevDelay + 300 },
    ]);
  };

  const removeLevel = (index: number) => {
    setLevels((prev) => prev.filter((_, i) => i !== index));
  };

  const handleSave = async () => {
    if (!token || !isValid) return;

    // Force first-level delay to 0
    const safeLevels: EscalationLevel[] = levels.map((lv, i) =>
      i === 0 ? { ...lv, delay_seconds: 0 } : lv
    );

    const input: EscalationPolicyInput = {
      name: name.trim(),
      description: description.trim(),
      min_severity: minSeverity,
      enabled,
      levels: safeLevels,
    };

    setSaving(true);
    setNameConflict(false);
    try {
      const saved = isEdit && policy
        ? await updateEscalationPolicy(token, policy.id, input)
        : await createEscalationPolicy(token, input);
      onSaved(saved);
      onOpenChange(false);
    } catch (err) {
      const msg = getErrorMessage(err);
      if (msg.includes("409") || msg.toLowerCase().includes("conflict")) {
        setNameConflict(true);
      } else {
        toast.error(msg);
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg" className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {isEdit ? t("escalation.editTitle", { name: policy?.name ?? "" }) : t("escalation.newButton")}
          </DialogTitle>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-4">
          {/* Name */}
          <div className="space-y-1">
            <label htmlFor="ep-name" className="text-sm font-medium">
              {t("escalation.fields.name")}
            </label>
            <Input
              id="ep-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setNameConflict(false);
              }}
              placeholder={t("escalation.placeholders.name")}
            />
            {errors.name && (
              <p className="text-xs text-destructive">{errors.name}</p>
            )}
            {nameConflict && (
              <p className="text-xs text-destructive">{t("escalation.errors.conflict")}</p>
            )}
          </div>

          {/* Description */}
          <div className="space-y-1">
            <label htmlFor="ep-description" className="text-sm font-medium">
              {t("escalation.fields.description")}
            </label>
            <Textarea
              id="ep-description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              placeholder={t("escalation.placeholders.description")}
            />
          </div>

          {/* Min severity */}
          <div className="space-y-1">
            <label htmlFor="ep-min-severity" className="text-sm font-medium">
              {t("escalation.fields.minSeverity")}
            </label>
            <Select
              id="ep-min-severity"
              value={minSeverity}
              onChange={(e) =>
                setMinSeverity(e.target.value as "info" | "warning" | "critical")
              }
            >
              <option value="info">{t("escalation.severity.info")}</option>
              <option value="warning">{t("escalation.severity.warning")}</option>
              <option value="critical">{t("escalation.severity.critical")}</option>
            </Select>
          </div>

          {/* Enabled */}
          <div className="flex items-center gap-3">
            <Switch
              id="ep-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
            />
            <label htmlFor="ep-enabled" className="text-sm font-medium cursor-pointer">
              {t("escalation.fields.enabled")}
            </label>
          </div>

          {/* Levels */}
          <div className="space-y-3">
            <p className="text-sm font-medium">{t("escalation.fields.levels")}</p>
            {levels.map((lv, i) => (
              <EscalationLevelRow
                key={i}
                level={lv}
                index={i}
                isFirst={i === 0}
                integrations={integrations}
                onChange={(next) => updateLevel(i, next)}
                onRemove={i > 0 ? () => removeLevel(i) : undefined}
                errors={errors.levels?.[i]}
              />
            ))}
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={addLevel}
              disabled={levels.length >= MAX_LEVELS}
            >
              <Plus className="size-4 mr-1" />
              {t("escalation.levels.addLevel")}
            </Button>
          </div>
        </DialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("escalation.actions.cancel")}
          </Button>
          <Button onClick={() => void handleSave()} disabled={!isValid || saving}>
            {saving ? t("escalation.actions.saving") : t("escalation.actions.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
