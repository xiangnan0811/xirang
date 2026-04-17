import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { InlineAlert } from "@/components/ui/inline-alert";
import type { IntegrationType } from "@/types/domain";

// 需要签名密钥的通道类型
export const SECRET_TYPES: ReadonlySet<IntegrationType> = new Set(["feishu", "dingtalk"]);

// 结构化通道类型（后端通过结构化字段构建 endpoint）
export const STRUCTURED_TYPES: ReadonlySet<IntegrationType> = new Set(["telegram", "dingtalk", "feishu", "wecom"]);

export type IntegrationGuide = {
  endpointPlaceholder: string;
  sample: string;
};

export const integrationGuideMap: Record<IntegrationType, IntegrationGuide> = {
  email: {
    endpointPlaceholder: "ops@example.com, oncall@example.com",
    sample: "ops@example.com",
  },
  slack: {
    endpointPlaceholder: "https://hooks.slack.com/services/xxx/yyy/zzz",
    sample: "https://hooks.slack.com/services/T000/B000/XXXX",
  },
  telegram: {
    endpointPlaceholder: "https://api.telegram.org/bot<token>/sendMessage?chat_id=<id>",
    sample: "https://api.telegram.org/bot123456:abc/sendMessage?chat_id=10001",
  },
  webhook: {
    endpointPlaceholder: "https://example.com/xirang/alerts",
    sample: "https://example.com/hooks/xirang",
  },
  feishu: {
    endpointPlaceholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    sample: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx",
  },
  dingtalk: {
    endpointPlaceholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    sample: "https://oapi.dingtalk.com/robot/send?access_token=xxxxxxxx",
  },
  wecom: {
    endpointPlaceholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    sample: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxxxxxx",
  },
};

export function isValidURL(value: string) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

export function validateEndpoint(type: IntegrationType, endpoint: string): string | null {
  const raw = endpoint.trim();
  if (!raw) return "integration.errorCreateEndpointRequired";
  if (type === "email") {
    const emails = raw.split(",").map((e) => e.trim()).filter(Boolean);
    if (!emails.length) return "integration.errorCreateEmailRequired";
    const mailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emails.every((e) => mailRegex.test(e))) return "integration.errorCreateEmailFormat";
    return null;
  }
  if (!isValidURL(raw)) return "integration.errorCreateUrlRequired";
  return null;
}

export type IntegrationFormDraft = {
  type: IntegrationType;
  endpoint: string;
  secret?: string;
  botToken?: string;
  chatId?: string;
  accessToken?: string;
  hookId?: string;
  webhookKey?: string;
  proxyUrl?: string;
};

type IntegrationFormFieldsProps = {
  draft: IntegrationFormDraft;
  idPrefix: string;
  onChange: (updater: (prev: IntegrationFormDraft) => IntegrationFormDraft) => void;
  pendingHint?: string | null;
  saving?: boolean;
  onDismissHint?: () => void;
  onConfirmHint?: () => void;
  /** When true, endpoint field shows but no sample hint row (editor mode) */
  editorMode?: boolean;
  /** Custom placeholder for the secret field (used in editor to show "leave blank to keep") */
  secretPlaceholder?: string;
};

export function IntegrationFormFields({
  draft,
  idPrefix,
  onChange,
  pendingHint,
  saving,
  onDismissHint,
  onConfirmHint,
  editorMode = false,
  secretPlaceholder,
}: IntegrationFormFieldsProps) {
  const { t } = useTranslation();
  const guide = integrationGuideMap[draft.type];
  const isStructured = STRUCTURED_TYPES.has(draft.type);
  const showSecretField = SECRET_TYPES.has(draft.type);

  return (
    <>
      {pendingHint && (
        <InlineAlert tone="warning" title={pendingHint} className="mb-2">
          <div className="mt-2 flex justify-end gap-2">
            <Button size="sm" variant="outline" onClick={onDismissHint}>
              {t("integration.recheck")}
            </Button>
            <Button size="sm" onClick={onConfirmHint} disabled={saving}>
              {t("integration.confirmSave")}
            </Button>
          </div>
        </InlineAlert>
      )}

      {isStructured ? (
        <>
          {draft.type === "telegram" && (
            <>
              <div>
                <label htmlFor={`${idPrefix}-bot-token`} className="mb-1 block text-sm font-medium">
                  Bot Token
                </label>
                <Input
                  id={`${idPrefix}-bot-token`}
                  autoComplete="off"
                  placeholder={t("integration.botTokenPlaceholder")}
                  value={draft.botToken ?? ""}
                  onChange={(e) => onChange((prev) => ({ ...prev, botToken: e.target.value }))}
                />
                <p className="mt-1 text-xs text-muted-foreground">{t("integration.botTokenHint")}</p>
              </div>
              <div>
                <label htmlFor={`${idPrefix}-chat-id`} className="mb-1 block text-sm font-medium">
                  Chat ID
                </label>
                <Input
                  id={`${idPrefix}-chat-id`}
                  placeholder={t("integration.chatIdPlaceholder")}
                  value={draft.chatId ?? ""}
                  onChange={(e) => onChange((prev) => ({ ...prev, chatId: e.target.value }))}
                />
                <p className="mt-1 text-xs text-muted-foreground">{t("integration.chatIdHint")}</p>
              </div>
            </>
          )}
          {draft.type === "dingtalk" && (
            <div>
              <label htmlFor={`${idPrefix}-access-token`} className="mb-1 block text-sm font-medium">
                Access Token
              </label>
              <Input
                id={`${idPrefix}-access-token`}
                autoComplete="off"
                placeholder={t("integration.accessTokenPlaceholder")}
                value={draft.accessToken ?? ""}
                onChange={(e) => onChange((prev) => ({ ...prev, accessToken: e.target.value }))}
              />
              <p className="mt-1 text-xs text-muted-foreground">{t("integration.endpointHints.dingtalk")}</p>
            </div>
          )}
          {draft.type === "feishu" && (
            <div>
              <label htmlFor={`${idPrefix}-hook-id`} className="mb-1 block text-sm font-medium">
                Hook ID
              </label>
              <Input
                id={`${idPrefix}-hook-id`}
                autoComplete="off"
                placeholder={t("integration.hookIdPlaceholder")}
                value={draft.hookId ?? ""}
                onChange={(e) => onChange((prev) => ({ ...prev, hookId: e.target.value }))}
              />
              <p className="mt-1 text-xs text-muted-foreground">{t("integration.endpointHints.feishu")}</p>
            </div>
          )}
          {draft.type === "wecom" && (
            <div>
              <label htmlFor={`${idPrefix}-webhook-key`} className="mb-1 block text-sm font-medium">
                Webhook Key
              </label>
              <Input
                id={`${idPrefix}-webhook-key`}
                autoComplete="off"
                placeholder={t("integration.webhookKeyPlaceholder")}
                value={draft.webhookKey ?? ""}
                onChange={(e) => onChange((prev) => ({ ...prev, webhookKey: e.target.value }))}
              />
              <p className="mt-1 text-xs text-muted-foreground">{t("integration.endpointHints.wecom")}</p>
            </div>
          )}
        </>
      ) : (
        <>
          <div>
            <label htmlFor={`${idPrefix}-endpoint`} className="mb-1 block text-sm font-medium">
              {editorMode
                ? t("integration.endpointAddress")
                : t(`integration.endpointLabels.${draft.type}`)}
            </label>
            <Input
              id={`${idPrefix}-endpoint`}
              placeholder={guide.endpointPlaceholder}
              value={draft.endpoint}
              onChange={(e) => onChange((prev) => ({ ...prev, endpoint: e.target.value }))}
            />
            <p className="mt-1 text-xs text-muted-foreground">
              {t(`integration.endpointHints.${draft.type}`)}
            </p>
          </div>

          {!editorMode && (
            <div className="flex items-center justify-between rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
              <span>{t("integration.sampleHint")}</span>
              <Button
                size="sm"
                variant="outline"
                onClick={() => onChange((prev) => ({ ...prev, endpoint: guide.sample }))}
              >
                {t("integration.applySample")}
              </Button>
            </div>
          )}
        </>
      )}

      {showSecretField && (
        <div>
          <label htmlFor={`${idPrefix}-secret`} className="mb-1 block text-sm font-medium">
            {t("integration.signingSecret")}
          </label>
          <Input
            id={`${idPrefix}-secret`}
            type="password"
            placeholder={secretPlaceholder ?? t("integration.signingSecretPlaceholder")}
            value={draft.secret ?? ""}
            onChange={(e) => onChange((prev) => ({ ...prev, secret: e.target.value }))}
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("integration.secretHint")}</p>
        </div>
      )}

      {draft.type !== "email" && (
        <div>
          <label htmlFor={`${idPrefix}-proxy`} className="mb-1 block text-sm font-medium">
            {t("integration.proxyUrl")}
          </label>
          <Input
            id={`${idPrefix}-proxy`}
            placeholder="http://proxy:8080 / socks5://proxy:1080"
            value={draft.proxyUrl ?? ""}
            onChange={(e) => onChange((prev) => ({ ...prev, proxyUrl: e.target.value }))}
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("integration.proxyHint")}</p>
        </div>
      )}
    </>
  );
}
