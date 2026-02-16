import { useEffect, useState } from "react";
import { Mail, MessageSquare, Save, Send, Webhook } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import type { IntegrationChannel, IntegrationType } from "@/types/domain";

type IntegrationEditorDraft = {
  id: string;
  type: IntegrationType;
  name: string;
  endpoint: string;
  failThreshold: number;
  cooldownMinutes: number;
};

type IntegrationEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  integration?: IntegrationChannel | null;
  onSave: (draft: IntegrationEditorDraft) => Promise<void>;
};

const emptyDraft: IntegrationEditorDraft = {
  id: "",
  type: "email",
  name: "",
  endpoint: "",
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

export function IntegrationEditorDialog({
  open,
  onOpenChange,
  integration,
  onSave,
}: IntegrationEditorDialogProps) {
  const [draft, setDraft] = useState<IntegrationEditorDraft>(emptyDraft);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) {
      setDraft(emptyDraft);
      return;
    }
    if (integration) {
      setDraft(toDraft(integration));
    }
  }, [integration, open]);

  const handleOpenChange = (next: boolean) => {
    onOpenChange(next);
  };

  const handleSave = async () => {
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
      });
    } catch (error) {
      toast.error((error as Error).message || "保存失败，请稍后重试。");
    } finally {
      setSaving(false);
    }
  };

  const TypeIcon = integrationIcon(draft.type);
  const typeLabel =
    draft.type === "email"
      ? "邮件"
      : draft.type === "slack"
        ? "Slack"
        : draft.type === "telegram"
          ? "Telegram"
          : "Webhook";

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <div className="flex items-center gap-2">
            <TypeIcon className="size-5 text-primary" />
            <DialogTitle>编辑通知方式</DialogTitle>
          </div>
          <DialogDescription>
            修改通知通道参数，保存后立即生效。
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-3">
          <div className="grid gap-3 md:grid-cols-2">
            <div>
              <label className="mb-1 block text-sm font-medium">通道类型</label>
              <Input value={typeLabel} disabled />
            </div>
            <div>
              <label className="mb-1 block text-sm font-medium">通道名称</label>
              <Input
                placeholder="例如：值班 Slack"
                value={draft.name}
                onChange={(event) =>
                  setDraft((prev) => ({ ...prev, name: event.target.value }))
                }
              />
            </div>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Endpoint / 地址</label>
            <Input
              value={draft.endpoint}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, endpoint: event.target.value }))
              }
            />
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <div>
              <label className="mb-1 block text-sm font-medium">告警阈值（失败次数）</label>
              <Input
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
              <label className="mb-1 block text-sm font-medium">冷却时间（分钟）</label>
              <Input
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
        </DialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => handleOpenChange(false)}>
            取消
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            <Save className="mr-1 size-4" />
            {saving ? "保存中..." : "保存修改"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export type { IntegrationEditorDraft };
