import { createContext, useContext, type ReactNode } from "react";
import type { NewSSHKeyInput, SSHKeyRecord } from "@/types/domain";

export interface SSHKeysContextValue {
  sshKeys: SSHKeyRecord[];
  refreshSSHKeys: () => Promise<void>;
  createSSHKey: (input: NewSSHKeyInput) => Promise<string>;
  updateSSHKey: (keyId: string, input: NewSSHKeyInput) => Promise<void>;
  deleteSSHKey: (keyId: string) => Promise<boolean>;
}

const SSHKeysContext = createContext<SSHKeysContextValue | null>(null);

export function useSSHKeysContext(): SSHKeysContextValue {
  const ctx = useContext(SSHKeysContext);
  if (!ctx) throw new Error("useSSHKeysContext must be used within SSHKeysContextProvider");
  return ctx;
}

export function SSHKeysContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: SSHKeysContextValue;
}) {
  return <SSHKeysContext.Provider value={value}>{children}</SSHKeysContext.Provider>;
}
