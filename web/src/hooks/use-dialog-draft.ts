import { type Dispatch, type SetStateAction, useEffect, useState } from "react";

export function useDialogDraft<T, E = unknown>(
  open: boolean,
  emptyDraft: T,
  editingEntity?: E | null,
  toDraftFn?: (entity: E) => T,
): [T, Dispatch<SetStateAction<T>>] {
  const [draft, setDraft] = useState<T>(emptyDraft);

  useEffect(() => {
    if (!open) {
      setDraft(emptyDraft);
      return;
    }
    setDraft(editingEntity && toDraftFn ? toDraftFn(editingEntity) : emptyDraft);
  }, [editingEntity, emptyDraft, open, toDraftFn]);

  return [draft, setDraft];
}
