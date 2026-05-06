import type { NewServiceMonitorInput, ServiceMonitor, StatusPageItem } from "@/types/domain";
import { request } from "./core";

const BASE_PATH = "/service-monitors";

export function createServiceMonitorsApi() {
  return {
    async list(token: string, signal?: AbortSignal): Promise<ServiceMonitor[]> {
      return (await request<ServiceMonitor[]>(BASE_PATH, { token, signal })) ?? [];
    },

    async get(token: string, id: number): Promise<ServiceMonitor> {
      return await request<ServiceMonitor>(`${BASE_PATH}/${id}`, { token });
    },

    async create(token: string, input: NewServiceMonitorInput): Promise<ServiceMonitor> {
      return await request<ServiceMonitor>(BASE_PATH, {
        method: "POST",
        body: input,
        token,
      });
    },

    async update(token: string, id: number, input: NewServiceMonitorInput): Promise<ServiceMonitor> {
      return await request<ServiceMonitor>(`${BASE_PATH}/${id}`, {
        method: "PUT",
        body: input,
        token,
      });
    },

    async delete(token: string, id: number): Promise<void> {
      await request<void>(`${BASE_PATH}/${id}`, { method: "DELETE", token });
    },

    /** Public endpoint — no auth required. */
    async getStatusPage(signal?: AbortSignal): Promise<StatusPageItem[]> {
      return (await request<StatusPageItem[]>("/status-page", { signal })) ?? [];
    },
  };
}
