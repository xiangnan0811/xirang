import { useTranslation } from "react-i18next";
import {
  Copy,
  Monitor,
  MoreHorizontal,
  Pencil,
  Plug,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { toast } from "@/components/ui/toast";
import type { SSHKeyRecord } from "@/types/domain";

export interface SSHKeyActionsMenuProps {
  sshKey: SSHKeyRecord;
  nodeCount: number;
  onEdit: (key: SSHKeyRecord) => void;
  onDelete: (key: SSHKeyRecord) => void;
  onTestConnection: (key: SSHKeyRecord) => void;
  onViewAssociatedNodes: (key: SSHKeyRecord) => void;
  onRotate: (key: SSHKeyRecord) => void;
}

export function SSHKeyActionsMenu({
  sshKey,
  nodeCount,
  onEdit,
  onDelete,
  onTestConnection,
  onViewAssociatedNodes,
  onRotate,
}: SSHKeyActionsMenuProps) {
  const { t } = useTranslation();

  const handleCopyPublicKey = async () => {
    if (!sshKey.publicKey) {
      toast.error(t("sshKeys.noPublicKey"));
      return;
    }
    try {
      await navigator.clipboard.writeText(sshKey.publicKey);
      toast.success(t("sshKeys.publicKeyCopied"));
    } catch {
      toast.error(t("sshKeys.copyFailed"));
    }
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="size-8 text-muted-foreground hover:text-foreground"
          aria-label={t("common.actions")}
        >
          <MoreHorizontal className="size-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        <DropdownMenuItem onClick={() => onEdit(sshKey)}>
          <Pencil className="mr-2 size-4" />
          {t("common.edit")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => void handleCopyPublicKey()}>
          <Copy className="mr-2 size-4" />
          {t("sshKeys.copyPublicKey")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onTestConnection(sshKey)}>
          <Plug className="mr-2 size-4" />
          {t("sshKeys.testConnection")}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onViewAssociatedNodes(sshKey)}>
          <Monitor className="mr-2 size-4" />
          {t("sshKeys.viewAssociatedNodes")}
          {nodeCount > 0 && (
            <Badge tone="success" className="ml-auto">
              {nodeCount}
            </Badge>
          )}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onRotate(sshKey)}>
          <RefreshCw className="mr-2 size-4" />
          {t("sshKeys.rotateKey")}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          className="text-destructive focus:text-destructive"
          onClick={() => onDelete(sshKey)}
        >
          <Trash2 className="mr-2 size-4" />
          {t("common.delete")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
