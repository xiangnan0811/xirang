import { useEffect, useState } from "react";

type PersistentStateOptions<T> = {
  deserialize?: (raw: string) => T;
  serialize?: (value: T) => string;
  syncAcrossTabs?: boolean;
};

const defaultDeserialize = <T,>(raw: string): T => JSON.parse(raw) as T;
const defaultSerialize = <T,>(value: T): string => JSON.stringify(value);

export function usePersistentState<T>(
  key: string,
  initialValue: T,
  options: PersistentStateOptions<T> = {}
) {
  const {
    deserialize = defaultDeserialize,
    serialize = defaultSerialize,
    syncAcrossTabs = true,
  } = options;

  const isBrowser = typeof window !== "undefined";

  const [state, setState] = useState<T>(() => {
    if (!isBrowser) {
      return initialValue;
    }
    try {
      const stored = window.localStorage.getItem(key);
      if (stored === null) {
        return initialValue;
      }
      return deserialize(stored);
    } catch {
      return initialValue;
    }
  });

  useEffect(() => {
    if (!isBrowser) {
      return;
    }
    try {
      window.localStorage.setItem(key, serialize(state));
    } catch {
      // 本地存储写入失败时忽略
    }
  }, [isBrowser, key, serialize, state]);

  useEffect(() => {
    if (!isBrowser || !syncAcrossTabs) {
      return;
    }

    const onStorage = (event: StorageEvent) => {
      if (event.key !== key) {
        return;
      }
      if (event.newValue === null) {
        setState(initialValue);
        return;
      }
      try {
        setState(deserialize(event.newValue));
      } catch {
        setState(initialValue);
      }
    };

    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, [deserialize, initialValue, isBrowser, key, syncAcrossTabs]);

  return [state, setState] as const;
}
