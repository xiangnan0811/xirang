import type { UserRecord } from "@/types/domain";
import { request } from "./core";

type UserResponse = {
  id: number;
  username: string;
  role: string;
  totp_enabled?: boolean;
};

function mapUser(row: UserResponse): UserRecord {
  return {
    id: row.id,
    username: row.username,
    role: row.role === "admin" || row.role === "operator" || row.role === "viewer" ? row.role : "viewer",
    totpEnabled: Boolean(row.totp_enabled)
  };
}

export function createUsersApi() {
  return {
    async getUsers(token: string): Promise<UserRecord[]> {
      const rows = (await request<UserResponse[]>("/users", { token })) ?? [];
      return rows.map((row) => mapUser(row));
    },

    async createUser(token: string, input: { username: string; password: string; role: UserRecord["role"] }): Promise<UserRecord> {
      const row = await request<UserResponse>("/users", {
        method: "POST",
        token,
        body: {
          username: input.username,
          password: input.password,
          role: input.role
        }
      });
      return mapUser(row);
    },

    async updateUser(token: string, userId: number, patch: { role?: UserRecord["role"]; password?: string }): Promise<UserRecord> {
      const row = await request<UserResponse>(`/users/${userId}`, {
        method: "PUT",
        token,
        body: {
          role: patch.role,
          password: patch.password
        }
      });
      return mapUser(row);
    },

    async deleteUser(token: string, userId: number): Promise<void> {
      await request(`/users/${userId}`, {
        method: "DELETE",
        token
      });
    }
  };
}
