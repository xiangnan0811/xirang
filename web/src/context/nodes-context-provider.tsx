import type { ReactNode } from "react";
import {
  NodesContext,
  type NodesContextValue,
} from "@/context/nodes-context.shared";

export function NodesContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: NodesContextValue;
}) {
  return <NodesContext.Provider value={value}>{children}</NodesContext.Provider>;
}
