import { request } from "./core"

export const retryDelivery = (token: string, id: number) =>
  request<void>(`/alert-deliveries/${id}/retry`, { method: "POST", token })
