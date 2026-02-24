import type { UserRecord } from "@/types/domain";
import { request, type Envelope, unwrapData } from "./core";

type UserResponse = {
  id: number;
  username: string;
  role: string;
};

function mapUser(row: UserResponse): UserRecord {
  return {
    id: row.id,
    username: row.username,
    role: row.role === "admin" || row.role === "operator" || row.role === "viewer" ? row.role : "viewer"
  };
}

export function createUsersApi() {
  return {
    async getUsers(token: string): Promise<UserRecord[]> {
      const payload = await request<Envelope<UserResponse[]>>("/users", { token });
      const rows = unwrapData(payload) ?? [];
      return rows.map((row) => mapUser(row));
    },

    async createUser(token: string, input: { username: string; password: string; role: UserRecord["role"] }): Promise<UserRecord> {
      const payload = await request<Envelope<UserResponse>>("/users", {
        method: "POST",
        token,
        body: {
          username: input.username,
          password: input.password,
          role: input.role
        }
      });
      return mapUser(unwrapData(payload));
    },

    async updateUser(token: string, userId: number, patch: { role?: UserRecord["role"]; password?: string }): Promise<UserRecord> {
      const payload = await request<Envelope<UserResponse>>(`/users/${userId}`, {
        method: "PUT",
        token,
        body: {
          role: patch.role,
          password: patch.password
        }
      });
      return mapUser(unwrapData(payload));
    },

    async deleteUser(token: string, userId: number): Promise<void> {
      await request(`/users/${userId}`, {
        method: "DELETE",
        token
      });
    }
  };
}
