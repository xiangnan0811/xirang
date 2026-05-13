import { createContext } from "react";
import type { NewSSHKeyInput, SSHKeyRecord } from "@/types/domain";

export interface SSHKeysContextValue {
  sshKeys: SSHKeyRecord[];
  refreshSSHKeys: () => Promise<void>;
  createSSHKey: (input: NewSSHKeyInput) => Promise<string>;
  updateSSHKey: (keyId: string, input: NewSSHKeyInput) => Promise<void>;
  deleteSSHKey: (keyId: string) => Promise<boolean>;
}

export const SSHKeysContext = createContext<SSHKeysContextValue | null>(null);
