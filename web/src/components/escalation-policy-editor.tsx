import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, Plus, RotateCcw } from "lucide-react";
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
import { toast } from "@/components/ui/toast-sonner";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { EscalationLevelRow, type EscalationLevelRowErrors } from "./escalation-level-row";
import { EscalationDraftPreview } from "./escalation-preview";
import { createIntegrationsApi } from "@/lib/api/integrations-api";
import { apiClient } from "@/lib/api/client";
import {
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthSessionGeneration,
  subscribeAuthTransition,
} from "@/lib/api/core";
import type { EscalationPolicyInput } from "@/lib/api/escalation";
import { getErrorMessage } from "@/lib/utils";
import { useAuth } from "@/context/auth-context.hooks";
import type { EscalationLevel, EscalationPolicy, IntegrationChannel } from "@/types/domain";

const intApi = createIntegrationsApi();

const MAX_LEVELS = 5;

const defaultLevel = (): EscalationLevel => ({
  delaySeconds: 0,
  integrationIds: [],
  severityOverride: "",
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
    if (i === 0 && lv.delaySeconds !== 0) {
      le.delay = t("escalation.validation.firstDelayMustBeZero");
    }
    if (i > 0 && lv.delaySeconds <= levels[i - 1].delaySeconds) {
      le.delay = t("escalation.validation.delayMustIncrease");
    }
    if (lv.integrationIds.length === 0) {
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
  onCloseAutoFocus?: DialogCloseAutoFocus;
};

export function EscalationPolicyEditor(props: Props) {
  const { token, role } = useAuth();
  const generation = useSyncExternalStore(
    subscribeAuthSessionGeneration,
    getAuthSessionGeneration,
    () => 0
  );
  const sessionKey = `${props.open}:${props.policy?.id ?? "new"}:${token ?? ""}:${role ?? ""}:${generation}`;

  return (
    <EscalationPolicySession
      key={sessionKey}
      {...props}
      token={token}
      role={role}
      generation={generation}
    />
  );
}

function EscalationPolicySession({
  open,
  onOpenChange,
  policy,
  onSaved,
  onCloseAutoFocus,
  token,
  role,
  generation,
}: Props & {
  token: string | null;
  role: string | null;
  generation: number;
}) {
  const { t } = useTranslation();

  const isEdit = Boolean(policy);

  const [name, setName] = useState(policy?.name ?? "");
  const [description, setDescription] = useState(policy?.description ?? "");
  const [minSeverity, setMinSeverity] = useState<"info" | "warning" | "critical">(
    policy?.minSeverity ?? "warning"
  );
  const [enabled, setEnabled] = useState(policy?.enabled ?? true);
  const [levels, setLevels] = useState<EscalationLevel[]>(() =>
    policy?.levels.length ? policy.levels : [defaultLevel()]
  );
  const [integrations, setIntegrations] = useState<IntegrationChannel[]>([]);
  const [integrationsLoading, setIntegrationsLoading] = useState(false);
  const [integrationsError, setIntegrationsError] = useState(false);
  const [integrationsReady, setIntegrationsReady] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveStarted, setSaveStarted] = useState(false);
  const [nameConflict, setNameConflict] = useState(false);

  const mountedRef = useRef(true);
  const intAttemptRef = useRef(0);
  const intPendingRef = useRef(false);
  const intControllerRef = useRef<AbortController | null>(null);

  const saveAttemptRef = useRef(0);
  const savePendingRef = useRef(false);

  // Compute errors on every render (controlled validation)
  const errors = validateForm(name, levels, t);
  const isValid = !hasErrors(errors) && !nameConflict;

  const allowed = useCallback(() => {
    if (!mountedRef.current || !open || !token || role !== "admin") return false;
    if (isAuthTransitionActive() || getAuthSessionGeneration() !== generation) return false;
    const snapshot = getAuthIdentitySnapshot();
    return snapshot.token === token && snapshot.role === "admin";
  }, [open, token, role, generation]);

  // Parent callbacks may change identity on every parent render; reading them through a ref keeps the
  // fence subscriptions below mounted for the whole session instead of re-running (and retiring every
  // in-flight request) on unrelated parent renders.
  const onOpenChangeRef = useRef(onOpenChange);
  useLayoutEffect(() => {
    onOpenChangeRef.current = onOpenChange;
  });

  // Synchronous layout cleanup and barrier/generation subscriptions
  useLayoutEffect(() => {
    mountedRef.current = true;

    const handleAuthRevocation = () => {
      intAttemptRef.current += 1;
      saveAttemptRef.current += 1;
      intPendingRef.current = false;
      savePendingRef.current = false;
      intControllerRef.current?.abort();
      intControllerRef.current = null;
      onOpenChangeRef.current(false);
    };

    const unsubscribeTransition = subscribeAuthTransition(() => {
      if (isAuthTransitionActive()) {
        handleAuthRevocation();
      }
    });

    const unsubscribeGeneration = subscribeAuthSessionGeneration(() => {
      handleAuthRevocation();
    });

    return () => {
      mountedRef.current = false;
      intAttemptRef.current += 1;
      saveAttemptRef.current += 1;
      intPendingRef.current = false;
      savePendingRef.current = false;
      intControllerRef.current?.abort();
      intControllerRef.current = null;
      unsubscribeTransition();
      unsubscribeGeneration();
    };
  }, []);

  // Load integrations with single-flight pendingRef, attempt fence, and retry support
  const loadIntegrations = useCallback(() => {
    if (!allowed() || intPendingRef.current) return;
    intAttemptRef.current += 1;
    const attempt = intAttemptRef.current;
    intControllerRef.current?.abort();
    const controller = new AbortController();
    intControllerRef.current = controller;
    intPendingRef.current = true;
    setIntegrationsLoading(true);
    setIntegrationsError(false);

    intApi.getIntegrations(token!, { signal: controller.signal }).then(
      (data) => {
        if (!mountedRef.current || intAttemptRef.current !== attempt) return;
        intPendingRef.current = false;
        intControllerRef.current = null;
        if (!allowed()) return;
        setIntegrations(data);
        setIntegrationsReady(true);
        setIntegrationsLoading(false);
      },
      () => {
        if (!mountedRef.current || intAttemptRef.current !== attempt) return;
        intPendingRef.current = false;
        intControllerRef.current = null;
        if (!allowed()) return;
        setIntegrationsError(true);
        setIntegrationsLoading(false);
      }
    );
  }, [allowed, token]);

  useEffect(() => {
    if (open) {
      loadIntegrations();
    }
  }, [open, loadIntegrations]);

  const updateLevel = (index: number, next: EscalationLevel) => {
    setLevels((prev) => prev.map((lv, i) => (i === index ? next : lv)));
  };

  const addLevel = () => {
    if (levels.length >= MAX_LEVELS) return;
    const prevDelay = levels[levels.length - 1]?.delaySeconds ?? 0;
    setLevels((prev) => [
      ...prev,
      { ...defaultLevel(), delaySeconds: prevDelay + 300 },
    ]);
  };

  const removeLevel = (index: number) => {
    setLevels((prev) => prev.filter((_, i) => i !== index));
  };

  const handleSave = async () => {
    if (!token || !isValid || saving || saveStarted || savePendingRef.current || !allowed()) return;

    savePendingRef.current = true;
    setSaveStarted(true);
    setSaving(true);
    setNameConflict(false);
    saveAttemptRef.current += 1;
    const attempt = saveAttemptRef.current;

    // Force first-level delay to 0
    const safeLevels: EscalationLevel[] = levels.map((lv, i) =>
      i === 0 ? { ...lv, delaySeconds: 0 } : lv
    );

    const input: EscalationPolicyInput = {
      name: name.trim(),
      description: description.trim(),
      minSeverity: minSeverity,
      enabled,
      levels: safeLevels,
    };

    try {
      const saved =
        isEdit && policy
          ? await apiClient.updateEscalationPolicy(token, policy.id, input)
          : await apiClient.createEscalationPolicy(token, input);

      if (!mountedRef.current || saveAttemptRef.current !== attempt) return;
      savePendingRef.current = false;
      if (!allowed()) return;

      onSaved(saved);
      onOpenChange(false);
    } catch (err) {
      if (!mountedRef.current || saveAttemptRef.current !== attempt) return;
      savePendingRef.current = false;
      if (!allowed()) return;

      const msg = getErrorMessage(err);
      if (msg.includes("409") || msg.toLowerCase().includes("conflict")) {
        setNameConflict(true);
      } else {
        toast.error(msg);
      }
    } finally {
      if (mountedRef.current && saveAttemptRef.current === attempt) {
        setSaving(false);
        setSaveStarted(false);
        savePendingRef.current = false;
      }
    }
  };

  const draftPolicyForPreview: EscalationPolicyInput = {
    name: name.trim(),
    description: description.trim(),
    minSeverity,
    enabled,
    levels: levels.map((lv, i) => (i === 0 ? { ...lv, delaySeconds: 0 } : lv)),
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg" className="max-h-[90vh] overflow-y-auto" onCloseAutoFocus={onCloseAutoFocus}>
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

          {/* Integrations loading / error banner */}
          {integrationsLoading && (
            <div role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              <span>{t("escalation.preview.integrationsLoading")}</span>
            </div>
          )}
          {integrationsError && (
            <div role="alert" className="flex flex-wrap items-center gap-2 text-xs text-destructive">
              <span>{t("escalation.preview.integrationsFailed")}</span>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 px-2 text-xs"
                onClick={() => void loadIntegrations()}
              >
                <RotateCcw className="size-3 mr-1" aria-hidden="true" />
                {t("escalation.preview.retryAction")}
              </Button>
            </div>
          )}

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
                integrationsReady={integrationsReady}
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

          {/* Escalation Draft Preview */}
          <EscalationDraftPreview
            policy={draftPolicyForPreview}
            token={token ?? ""}
            active={open && !saving && !saveStarted}
            disabled={saving || saveStarted || integrationsLoading || integrationsError}
            integrationsFailed={integrationsError}
          />
        </DialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("escalation.actions.cancel")}
          </Button>
          <Button onClick={() => void handleSave()} disabled={!isValid || saving || saveStarted}>
            {saving ? t("escalation.actions.saving") : t("escalation.actions.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
