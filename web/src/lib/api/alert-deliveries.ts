import { parseNumericId, request } from "./core"

export function createAlertDeliveriesApi() {
  return {
    async retryDelivery(token: string, id: string | number): Promise<void> {
      const numericId = typeof id === "number" ? id : parseNumericId(String(id), "delivery");
      return request<void>(`/alert-deliveries/${numericId}/retry`, { method: "POST", token })
    },
  }
}
