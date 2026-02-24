import type { NewSSHKeyInput, SSHKeyRecord } from "@/types/domain";
import { formatTime, parseNumericId, request, type Envelope, unwrapData } from "./core";

type SSHKeyResponse = {
  id: number;
  name: string;
  username: string;
  key_type?: "auto" | "rsa" | "ed25519" | "ecdsa";
  private_key?: string;
  fingerprint: string;
  created_at: string;
  last_used_at?: string | null;
};

function mapSSHKey(row: SSHKeyResponse): SSHKeyRecord {
  return {
    id: `key-${row.id}`,
    name: row.name,
    username: row.username,
    keyType: row.key_type ?? "auto",
    fingerprint: row.fingerprint,
    createdAt: formatTime(row.created_at),
    lastUsedAt: formatTime(row.last_used_at)
  };
}

export function createSSHKeysApi() {
  return {
    async getSSHKeys(token: string, options?: { signal?: AbortSignal }): Promise<SSHKeyRecord[]> {
      const payload = await request<Envelope<SSHKeyResponse[]>>("/ssh-keys", { token, signal: options?.signal });
      const rows = unwrapData(payload) ?? [];
      return rows.map((row) => mapSSHKey(row));
    },

    async createSSHKey(token: string, input: NewSSHKeyInput): Promise<SSHKeyRecord> {
      const privateKey = input.privateKey.trim();
      const payload = await request<Envelope<SSHKeyResponse>>("/ssh-keys", {
        method: "POST",
        token,
        body: {
          name: input.name,
          username: input.username,
          key_type: input.keyType,
          private_key: privateKey
        }
      });
      return mapSSHKey(unwrapData(payload));
    },

    async updateSSHKey(token: string, keyId: string, input: NewSSHKeyInput): Promise<SSHKeyRecord> {
      const numericId = parseNumericId(keyId, "key");
      const privateKey = input.privateKey.trim();
      const payload = await request<Envelope<SSHKeyResponse>>(`/ssh-keys/${numericId}`, {
        method: "PUT",
        token,
        body: {
          name: input.name,
          username: input.username,
          key_type: input.keyType,
          ...(privateKey ? { private_key: privateKey } : {})
        }
      });
      return mapSSHKey(unwrapData(payload));
    },

    async deleteSSHKey(token: string, keyId: string): Promise<void> {
      const numericId = parseNumericId(keyId, "key");
      await request(`/ssh-keys/${numericId}`, {
        method: "DELETE",
        token
      });
    }
  };
}
