import { request } from "./core"

export const retryDelivery = (token: string, id: string | number) =>
  request<void>(`/alert-deliveries/${id}/retry`, { method: "POST", token })
