import { useState } from "react";
import { AlertTriangle, Bell, Building2, Mail, MessageSquare, Send, Webhook } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { EndpointHintWarning } from "@/lib/api/integrations-api";
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
  feishu: {
    endpointLabel: "飞书 Webhook URL",
    endpointPlaceholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    endpointHint: "在飞书群自定义机器人中复制 Webhook 地址。如启用签名校验，请同时填写签名密钥。",
    sample: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx",
  },
  dingtalk: {
    endpointLabel: "钉钉 Webhook URL",
    endpointPlaceholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    endpointHint: "在钉钉自定义机器人中复制 Webhook 地址。如启用加签安全设置，请同时填写签名密钥。",
    sample: "https://oapi.dingtalk.com/robot/send?access_token=xxxxxxxx",
  },
  wecom: {
    endpointLabel: "企业微信机器人 Webhook URL",
    endpointPlaceholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    endpointHint: "在企业微信群机器人中复制 Webhook 地址。",
    sample: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxxxx",
  },
};

const typeIconMap: Record<IntegrationType, typeof Mail> = {
  email: Mail,
  slack: MessageSquare,
  telegram: Send,
  webhook: Webhook,
  feishu: MessageSquare,
  dingtalk: Bell,
  wecom: Building2,
};

// 需要签名密钥的通道类型
const SECRET_TYPES: ReadonlySet<IntegrationType> = new Set(["feishu", "dingtalk"]);

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

const KNOWN_TYPES: ReadonlySet<string> = new Set<IntegrationType>([
  "email", "slack", "telegram", "webhook", "feishu", "dingtalk", "wecom",
]);

function toIntegrationType(value: string): IntegrationType {
  return KNOWN_TYPES.has(value) ? (value as IntegrationType) : "email";
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
  const [pendingHint, setPendingHint] = useState<string | null>(null);

  const guide = integrationGuideMap[draft.type];
  const TypeIcon = typeIconMap[draft.type];
  const showSecretField = SECRET_TYPES.has(draft.type);

  const handleSave = async (skipHint = false) => {
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
        secret: draft.secret?.trim() || undefined,
        failThreshold: toBoundedInt(String(draft.failThreshold), 2, 1, 10),
        cooldownMinutes: toBoundedInt(String(draft.cooldownMinutes), 5, 1, 120),
        skipEndpointHint: skipHint,
      });
      setPendingHint(null);
    } catch (error) {
      if (error instanceof EndpointHintWarning) {
        setPendingHint(error.hint);
      } else {
        toast.error(getErrorMessage(error, "新增失败，请稍后重试。"));
      }
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
      description="配置告警通知通道，支持邮件、Slack、Telegram、飞书、钉钉、企业微信及自定义 Webhook。"
      saving={saving}
      onSubmit={() => handleSave(false)}
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
                secret: "",
              }))
            }
          >
            <option value="email">邮件</option>
            <option value="slack">Slack</option>
            <option value="telegram">Telegram</option>
            <option value="webhook">Webhook</option>
            <option value="feishu">飞书</option>
            <option value="dingtalk">钉钉</option>
            <option value="wecom">企业微信</option>
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

      {showSecretField && (
        <div>
          <label htmlFor="create-integration-secret" className="mb-1 block text-sm font-medium">
            签名密钥（可选）
          </label>
          <Input
            id="create-integration-secret"
            type="password"
            placeholder="填写机器人安全设置中的签名密钥"
            value={draft.secret ?? ""}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, secret: event.target.value }))
            }
          />
          <p className="mt-1 text-xs text-muted-foreground">
            仅在机器人启用了加签安全设置时填写，留空则不使用签名。
          </p>
        </div>
      )}

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
