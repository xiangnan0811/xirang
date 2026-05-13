import type { ReactNode } from "react";
import {
  SSHKeysContext,
  type SSHKeysContextValue,
} from "@/context/ssh-keys-context.shared";

export function SSHKeysContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: SSHKeysContextValue;
}) {
  return <SSHKeysContext.Provider value={value}>{children}</SSHKeysContext.Provider>;
}
