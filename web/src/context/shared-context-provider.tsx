import type { ReactNode } from "react";
import {
  SharedContext,
  type SharedContextValue,
} from "@/context/shared-context.shared";

export function SharedContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: SharedContextValue;
}) {
  return <SharedContext.Provider value={value}>{children}</SharedContext.Provider>;
}
