import { useState } from "react";
import { Mail, MessageSquare, Send, Webhook } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import type { IntegrationType, NewIntegrationInput } from "@/types/domain";

type IntegrationGuide = {
  endpointLabel: string;
  endpointPlaceholder: string;
  endpointHint: string;
  sample: string;
};

const integrationGuideMap: Record<IntegrationType, IntegrationGuide> = {
  email: {
    endpointLabel: "收件邮箱",
    endpointPlaceholder: "ops@example.com, oncall@example.com",
    endpointHint: "可填写多个邮箱，使用逗号分隔。",
    sample: "ops@example.com",
  },
  slack: {
    endpointLabel: "Slack Webhook URL",
    endpointPlaceholder: "https://hooks.slack.com/services/xxx/yyy/zzz",
    endpointHint: "请在 Slack Incoming Webhook 中复制地址。",
    sample: "https://hooks.slack.com/services/T000/B000/XXXX",
  },
  telegram: {
    endpointLabel: "Telegram Bot Endpoint",
    endpointPlaceholder:
      "https://api.telegram.org/bot<token>/sendMessage?chat_id=<id>",
    endpointHint: "建议使用机器人 sendMessage 接口完整 URL。",
    sample:
      "https://api.telegram.org/bot123456:abc/sendMessage?chat_id=10001",
  },
  webhook: {
    endpointLabel: "Webhook URL",
    endpointPlaceholder: "https://example.com/xirang/alerts",
    endpointHint: "支持任意 HTTP/HTTPS 接收端点。",
    sample: "https://example.com/hooks/xirang",
  },
};

const typeIconMap: Record<IntegrationType, typeof Mail> = {
  email: Mail,
  slack: MessageSquare,
  telegram: Send,
  webhook: Webhook,
};

const defaultDraft: NewIntegrationInput = {
  type: "email",
  name: "",
  endpoint: "",
  failThreshold: 2,
  cooldownMinutes: 5,
  enabled: true,
};

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
}

function toIntegrationType(value: string): IntegrationType {
  if (value === "slack" || value === "telegram" || value === "webhook") {
    return value;
  }
  return "email";
}

function isValidURL(value: string) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

function validateDraft(
  type: IntegrationType,
  endpoint: string
): string | null {
  const raw = endpoint.trim();
  if (!raw) {
    return "新增失败：请填写通知地址。";
  }

  if (type === "email") {
    const emails = raw
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
    if (!emails.length) {
      return "新增失败：请填写至少一个邮箱地址。";
    }
    const mailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emails.every((item) => mailRegex.test(item))) {
      return "新增失败：邮箱格式不正确，请使用逗号分隔多个邮箱。";
    }
    return null;
  }

  if (!isValidURL(raw)) {
    return "新增失败：该通道需要合法的 http/https 地址。";
  }
  return null;
}

type IntegrationCreateDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSave: (input: NewIntegrationInput) => Promise<void>;
};

export function IntegrationCreateDialog({
  open,
  onOpenChange,
  onSave,
}: IntegrationCreateDialogProps) {
  const [draft, setDraft] = useDialogDraft<NewIntegrationInput>(open, defaultDraft);
  const [saving, setSaving] = useState(false);

  const guide = integrationGuideMap[draft.type];
  const TypeIcon = typeIconMap[draft.type];

  const handleSave = async () => {
    if (!draft.name.trim()) {
      toast.error("新增失败：请填写通道名称。");
      return;
    }

    const validationError = validateDraft(draft.type, draft.endpoint);
    if (validationError) {
      toast.error(validationError);
      return;
    }

    setSaving(true);
    try {
      await onSave({
        ...draft,
        name: draft.name.trim(),
        endpoint: draft.endpoint.trim(),
        failThreshold: toBoundedInt(String(draft.failThreshold), 2, 1, 10),
        cooldownMinutes: toBoundedInt(String(draft.cooldownMinutes), 5, 1, 120),
      });
    } catch (error) {
      toast.error(getErrorMessage(error, "新增失败，请稍后重试。"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={<TypeIcon className="size-5 text-primary" />}
      title="新增通知方式"
      description="配置告警通知通道，支持邮件、Slack、Telegram 及自定义 Webhook。"
      saving={saving}
      onSubmit={handleSave}
      submitLabel="保存通道"
    >
      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="create-integration-type" className="mb-1 block text-sm font-medium">
            通道类型
          </label>
          <AppSelect
            id="create-integration-type"
            containerClassName="w-full"
            value={draft.type}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                type: toIntegrationType(event.target.value),
              }))
            }
          >
            <option value="email">邮件</option>
            <option value="slack">Slack</option>
            <option value="telegram">Telegram</option>
            <option value="webhook">Webhook</option>
          </AppSelect>
        </div>

        <div>
          <label htmlFor="create-integration-name" className="mb-1 block text-sm font-medium">
            通道名称
          </label>
          <Input
            id="create-integration-name"
            placeholder="例如：运维邮箱、值班 Slack"
            value={draft.name}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, name: event.target.value }))
            }
          />
        </div>
      </div>

      <div>
        <label htmlFor="create-integration-endpoint" className="mb-1 block text-sm font-medium">
          {guide.endpointLabel}
        </label>
        <Input
          id="create-integration-endpoint"
          placeholder={guide.endpointPlaceholder}
          value={draft.endpoint}
          onChange={(event) =>
            setDraft((prev) => ({
              ...prev,
              endpoint: event.target.value,
            }))
          }
        />
        <p className="mt-1 text-xs text-muted-foreground">
          {guide.endpointHint}
        </p>
      </div>

      <div className="flex items-center justify-between rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
        <span>可直接套用示例地址后再修改。</span>
        <Button
          size="sm"
          variant="outline"
          onClick={() =>
            setDraft((prev) => ({ ...prev, endpoint: guide.sample }))
          }
        >
          套用示例
        </Button>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="create-integration-fail-threshold" className="mb-1 block text-sm font-medium">
            失败阈值（次数）
          </label>
          <Input
            id="create-integration-fail-threshold"
            type="number"
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
          <label htmlFor="create-integration-cooldown" className="mb-1 block text-sm font-medium">
            冷却时间（分钟）
          </label>
          <Input
            id="create-integration-cooldown"
            type="number"
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
