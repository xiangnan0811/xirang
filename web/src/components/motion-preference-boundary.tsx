import { MotionConfig } from "framer-motion";
import type { PropsWithChildren } from "react";
import { useTheme } from "@/context/theme-context.hooks";

export function MotionPreferenceBoundary({ children }: PropsWithChildren) {
  const { powerMode } = useTheme();

  return (
    <MotionConfig reducedMotion={powerMode === "save" ? "always" : "user"}>
      {children}
    </MotionConfig>
  );
}
