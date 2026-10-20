import { createContext } from "react";
import type { SSHKeyReplacementOptions } from "@/lib/ssh-key-replacement";
import type { NewSSHKeyInput, SSHKeyRecord } from "@/types/domain";

export interface SSHKeysContextValue {
  sshKeys: SSHKeyRecord[];
  refreshSSHKeys: () => Promise<void>;
  createSSHKey: (input: NewSSHKeyInput, isCurrent?: () => boolean) => Promise<string>;
  updateSSHKey: (keyId: string, input: NewSSHKeyInput, options: SSHKeyReplacementOptions) => Promise<void>;
  deleteSSHKey: (keyId: string) => Promise<boolean>;
}

export const SSHKeysContext = createContext<SSHKeysContextValue | null>(null);
