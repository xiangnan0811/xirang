import { useContext } from "react";
import i18n from "@/i18n";
import { AuthContext } from "@/context/auth-context.shared";

export function useAuth() {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error(i18n.t("context.useAuthError"));
  }
  return context;
}
