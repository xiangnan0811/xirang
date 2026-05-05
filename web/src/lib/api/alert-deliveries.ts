import { request } from "./core"

export function createAlertDeliveriesApi() {
  return {
    async retryDelivery(token: string, id: string | number): Promise<void> {
      return request<void>(`/alert-deliveries/${id}/retry`, { method: "POST", token })
    },
  }
}
