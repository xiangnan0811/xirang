import { useCallback, useEffect, useRef, useState } from "react";
import i18n from "@/i18n";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle
} from "@/components/ui/confirm-dialog";

type ConfirmOptions = {
  title: string;
  description: string;
  confirmText?: string;
  cancelText?: string;
};

type ConfirmRequest = ConfirmOptions & {
  id: number;
  resolve: (value: boolean) => void;
};

export function useConfirm() {
  const [state, setState] = useState<ConfirmRequest | null>(null);
  const queueRef = useRef<ConfirmRequest[]>([]);
  const currentRef = useRef<ConfirmRequest | null>(null);
  const idRef = useRef(1);

  const openNext = useCallback(() => {
    const next = queueRef.current.shift() ?? null;
    currentRef.current = next;
    setState(next);
  }, []);

  const confirm = useCallback(
    (options: ConfirmOptions): Promise<boolean> =>
      new Promise((resolve) => {
        const request: ConfirmRequest = {
          ...options,
          id: idRef.current++,
          resolve
        };

        if (!currentRef.current) {
          currentRef.current = request;
          setState(request);
          return;
        }

        queueRef.current.push(request);
      }),
    []
  );

  const handleClose = useCallback(
    (value: boolean, requestId: number) => {
      const active = currentRef.current;
      if (!active || active.id !== requestId) {
        return;
      }
      active.resolve(value);
      openNext();
    },
    [openNext]
  );

  useEffect(() => {
    return () => {
      currentRef.current?.resolve(false);
      for (const pending of queueRef.current) {
        pending.resolve(false);
      }
      queueRef.current = [];
      currentRef.current = null;
    };
  }, []);

  const dialog = state ? (
    <AlertDialog
      open
      onOpenChange={(open) => {
        if (!open) {
          handleClose(false, state.id);
        }
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{state.title}</AlertDialogTitle>
          <AlertDialogDescription>{state.description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => handleClose(false, state.id)}>
            {state.cancelText ?? i18n.t("common.cancel")}
          </AlertDialogCancel>
          <AlertDialogAction onClick={() => handleClose(true, state.id)}>
            {state.confirmText ?? i18n.t("common.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  ) : null;

  return { confirm, dialog };
}
