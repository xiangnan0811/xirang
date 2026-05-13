import { motion, AnimatePresence } from "framer-motion";
import type { ReactNode } from "react";
import { pageTransition, pageVariants } from "@/components/ui/motion.constants";

export function PageTransition({ children, layoutKey }: { children: ReactNode; layoutKey: string }) {
  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={layoutKey}
        initial="initial"
        animate="animate"
        exit="exit"
        variants={pageVariants}
        transition={pageTransition}
      >
        {children}
      </motion.div>
    </AnimatePresence>
  );
}
