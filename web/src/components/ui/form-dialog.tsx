import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
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

type FormDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  icon?: ReactNode;
  size?: "sm" | "md" | "lg";
  saving: boolean;
  onSubmit: () => void | Promise<void>;
  submitLabel: ReactNode;
  savingLabel?: ReactNode;
  extraFooter?: ReactNode;
  children: ReactNode;
};

export function FormDialog({
  open,
  onOpenChange,
  title,
  description,
  icon,
  size = "md",
  saving,
  onSubmit,
  submitLabel,
  savingLabel,
  extraFooter,
  children,
}: FormDialogProps) {
  const { t } = useTranslation();
  const resolvedSavingLabel = savingLabel ?? t('common.saving');
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size={size}>
        <DialogHeader>
          {icon ? (
            <div className="flex items-center gap-2">
              {icon}
              <DialogTitle>{title}</DialogTitle>
            </div>
          ) : (
            <DialogTitle>{title}</DialogTitle>
          )}
          {description ? <DialogDescription>{description}</DialogDescription> : null}
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-3">
          {children}
        </DialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          {extraFooter}
          <Button onClick={() => void onSubmit()} disabled={saving}>
            {saving ? resolvedSavingLabel : submitLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
