import { useContext } from "react";
import { SSHKeysContext, type SSHKeysContextValue } from "@/context/ssh-keys-context.shared";

export function useSSHKeysContext(): SSHKeysContextValue {
  const ctx = useContext(SSHKeysContext);
  if (!ctx) throw new Error("useSSHKeysContext must be used within SSHKeysContextProvider");
  return ctx;
}
