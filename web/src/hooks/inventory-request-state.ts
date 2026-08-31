export type InventoryRequestState = {
  loading: boolean;
  error: string | null;
  loaded: boolean;
};

export const idleInventoryRequestState: InventoryRequestState = {
  loading: false,
  error: null,
  loaded: false,
};

export function isAbortError(error: unknown): boolean {
  return (
    (typeof DOMException !== "undefined" && error instanceof DOMException && error.name === "AbortError") ||
    (error instanceof Error && error.name === "AbortError")
  );
}
