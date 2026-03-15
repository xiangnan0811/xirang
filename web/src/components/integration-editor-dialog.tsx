import { useState } from "react";
import { AlertTriangle, Bell, Building2, Mail, MessageSquare, Save, Send, Webhook } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { EndpointHintWarning } from "@/lib/api/integrations-api";
import type { IntegrationChannel, IntegrationType } from "@/types/domain";

type IntegrationEditorDraft = {
  id: string;
  type: IntegrationType;
  name: string;
  endpoint: string;
  secret: string;
  failThreshold: number;
  cooldownMinutes: number;
  skipEndpointHint?: boolean;
};

const emptyDraft: IntegrationEditorDraft = {
  id: "",
  type: "email",
  name: "",
  endpoint: "",
  secret: "",
  failThreshold: 1,
  cooldownMinutes: 5,
};

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
}

function toDraft(integration: IntegrationChannel): IntegrationEditorDraft {
  return {
    id: integration.id,
    type: integration.type,
    name: integration.name,
    endpoint: integration.endpoint,
    secret: "",
    failThreshold: integration.failThreshold,
    cooldownMinutes: integration.cooldownMinutes,
  };
}

function integrationIcon(type: IntegrationType) {
  switch (type) {
    case "email":
      return Mail;
    case "slack":
      return MessageSquare;
    case "telegram":
      return Send;
    case "feishu":
      return MessageSquare;
    case "dingtalk":
      return Bell;
    case "wecom":
      return Building2;
    default:
      return Webhook;
  }
}

function isValidURL(value: string) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

function validateDraft(type: IntegrationType, endpoint: string): string | null {
  const raw = endpoint.trim();
  if (!raw) {
    return "保存失败：请填写通知地址。";
  }

  if (type === "email") {
    const emails = raw
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
    if (!emails.length) {
      return "保存失败：请填写至少一个邮箱地址。";
    }
    const mailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emails.every((item) => mailRegex.test(item))) {
      return "保存失败：邮箱格式不正确，请使用逗号分隔多个邮箱。";
    }
    return null;
  }

  if (!isValidURL(raw)) {
    return "保存失败：该通道需要合法的 http/https 地址。";
  }
  return null;
}

type IntegrationEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  integration?: IntegrationChannel | null;
  onSave: (draft: IntegrationEditorDraft) => Promise<void>;
};

export function IntegrationEditorDialog({
  open,
  onOpenChange,
  integration,
  onSave,
}: IntegrationEditorDialogProps) {
  const [draft, setDraft] = useDialogDraft<IntegrationEditorDraft, IntegrationChannel>(open, emptyDraft, integration, toDraft);
  const [saving, setSaving] = useState(false);
  const [pendingHint, setPendingHint] = useState<string | null>(null);

  const handleSave = async (skipHint: boolean) => {
    if (!draft.name.trim()) {
      toast.error("保存失败：请填写通道名称。", { id: "integration-edit-name-required" });
      return;
    }

    const validationError = validateDraft(draft.type, draft.endpoint);
    if (validationError) {
      toast.error(validationError, { id: "integration-edit-endpoint-invalid" });
      return;
    }

    setSaving(true);
    try {
      await onSave({
        ...draft,
        name: draft.name.trim(),
        endpoint: draft.endpoint.trim(),
        failThreshold: toBoundedInt(String(draft.failThreshold), 1, 1, 10),
        cooldownMinutes: toBoundedInt(String(draft.cooldownMinutes), 5, 1, 120),
        skipEndpointHint: skipHint,
      });
      setPendingHint(null);
    } catch (error) {
      if (error instanceof EndpointHintWarning) {
        setPendingHint(error.hint);
      } else {
        toast.error(getErrorMessage(error, "保存失败，请稍后重试。"));
      }
    } finally {
      setSaving(false);
    }
  };

  const TypeIcon = integrationIcon(draft.type);
  const typeLabel =
    draft.type === "email" ? "邮件" :
    draft.type === "slack" ? "Slack" :
    draft.type === "telegram" ? "Telegram" :
    draft.type === "feishu" ? "飞书" :
    draft.type === "dingtalk" ? "钉钉" :
    draft.type === "wecom" ? "企业微信" :
    "Webhook";

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={<TypeIcon className="size-5 text-primary" />}
      title="编辑通知方式"
      description="修改通知通道参数，保存后立即生效。"
      saving={saving}
      onSubmit={() => handleSave(false)}
      submitLabel={<><Save className="mr-1 size-4" />保存修改</>}
      savingLabel={<><Save className="mr-1 size-4" />保存中...</>}
    >
      {pendingHint && (
        <div className="flex flex-col gap-2 rounded-md border border-yellow-200 bg-yellow-50 px-3 py-2 dark:border-yellow-800 dark:bg-yellow-950">
          <div className="flex items-start gap-2 text-sm text-yellow-800 dark:text-yellow-200">
            <AlertTriangle className="mt-0.5 size-4 shrink-0" />
            <span>{pendingHint}</span>
          </div>
          <div className="flex justify-end gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => setPendingHint(null)}
            >
              重新检查
            </Button>
            <Button
              size="sm"
              onClick={() => handleSave(true)}
              disabled={saving}
            >
              确认保存
            </Button>
          </div>
        </div>
      )}

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="int-edit-type" className="mb-1 block text-sm font-medium">通道类型</label>
          <Input id="int-edit-type" value={typeLabel} disabled className="glass-panel" />
        </div>
        <div>
          <label htmlFor="int-edit-name" className="mb-1 block text-sm font-medium">通道名称</label>
          <Input id="int-edit-name" placeholder="例如：值班 Slack"
            value={draft.name}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, name: event.target.value }))
            }
          />
        </div>
      </div>

      <div>
        <label htmlFor="int-edit-endpoint" className="mb-1 block text-sm font-medium">Endpoint / 地址</label>
        <Input id="int-edit-endpoint" value={draft.endpoint}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, endpoint: event.target.value }))
          }
        />
      </div>

      {draft.type !== "email" && (
        <div>
          <label htmlFor="int-edit-secret" className="mb-1 block text-sm font-medium">签名密钥（选填）</label>
          <Input id="int-edit-secret" type="password" autoComplete="off"
            placeholder={draft.id ? "留空保持不变" : "用于验签的密钥"}
            value={draft.secret}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, secret: event.target.value }))
            }
          />
        </div>
      )}

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="int-edit-threshold" className="mb-1 block text-sm font-medium">告警阈值（失败次数）</label>
          <Input id="int-edit-threshold" type="number"
            min={1}
            max={10}
            value={draft.failThreshold}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                failThreshold: toBoundedInt(event.target.value, 1, 1, 10),
              }))
            }
          />
        </div>
        <div>
          <label htmlFor="int-edit-cooldown" className="mb-1 block text-sm font-medium">冷却时间（分钟）</label>
          <Input id="int-edit-cooldown" type="number"
            min={1}
            max={120}
            value={draft.cooldownMinutes}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                cooldownMinutes: toBoundedInt(event.target.value, 1, 1, 120),
              }))
            }
          />
        </div>
      </div>
    </FormDialog>
  );
}

export type { IntegrationEditorDraft };
