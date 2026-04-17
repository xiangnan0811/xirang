import { useTranslation } from "react-i18next";
import { Bell, Building2, Mail, MessageSquare, Send, Webhook } from "lucide-react";
import { Select } from "@/components/ui/select";
import type { IntegrationType } from "@/types/domain";

const CHANNEL_OPTIONS: { value: IntegrationType; icon: typeof Mail }[] = [
  { value: "email", icon: Mail },
  { value: "slack", icon: MessageSquare },
  { value: "telegram", icon: Send },
  { value: "webhook", icon: Webhook },
  { value: "feishu", icon: MessageSquare },
  { value: "dingtalk", icon: Bell },
  { value: "wecom", icon: Building2 },
];

export { CHANNEL_OPTIONS };

export const typeIconMap: Record<IntegrationType, typeof Mail> = {
  email: Mail,
  slack: MessageSquare,
  telegram: Send,
  webhook: Webhook,
  feishu: MessageSquare,
  dingtalk: Bell,
  wecom: Building2,
};

export const KNOWN_TYPES: ReadonlySet<string> = new Set<IntegrationType>([
  "email", "slack", "telegram", "webhook", "feishu", "dingtalk", "wecom",
]);

export function toIntegrationType(value: string): IntegrationType {
  return KNOWN_TYPES.has(value) ? (value as IntegrationType) : "email";
}

type ChannelPickerProps = {
  id?: string;
  value: IntegrationType;
  onChange: (type: IntegrationType) => void;
  disabled?: boolean;
};

export function ChannelPicker({ id, value, onChange, disabled }: ChannelPickerProps) {
  const { t } = useTranslation();

  return (
    <Select
      id={id}
      containerClassName="w-full"
      value={value}
      disabled={disabled}
      onChange={(event) => onChange(toIntegrationType(event.target.value))}
    >
      {CHANNEL_OPTIONS.map((opt) => (
        <option key={opt.value} value={opt.value}>
          {t(`integration.typeLabels.${opt.value}`)}
        </option>
      ))}
    </Select>
  );
}
