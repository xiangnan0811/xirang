import { motion, AnimatePresence } from "framer-motion";
import type { ReactNode } from "react";

export const pageVariants = {
  initial: { opacity: 0, y: 8 },
  animate: { opacity: 1, y: 0 },
  exit: { opacity: 0 }
};

export const pageTransition = {
  duration: 0.2,
  ease: [0, 0, 0.2, 1] as const
};

export const staggerContainer = {
  animate: { transition: { staggerChildren: 0.05 } }
};

export const staggerItem = {
  initial: { opacity: 0, y: 8 },
  animate: { opacity: 1, y: 0, transition: { duration: 0.2, ease: [0, 0, 0.2, 1] } }
};

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

export { motion, AnimatePresence };
