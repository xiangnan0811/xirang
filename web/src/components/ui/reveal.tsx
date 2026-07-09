import type { ReactNode } from "react";
import { motion } from "framer-motion";

const container = {
  animate: {
    transition: { delayChildren: 0.04, staggerChildren: 0.08 },
  },
};

const item = {
  initial: { opacity: 0, y: 16 },
  animate: {
    opacity: 1,
    y: 0,
    transition: { duration: 0.35, ease: [0.22, 1, 0.36, 1] as const },
  },
};

/**
 * Staggers the entrance of its direct <Reveal> children. Used to give dashboard
 * sections a coordinated, premium reveal. Respects reduced-motion via MotionConfig.
 */
export function Stagger({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <motion.div initial="initial" animate="animate" variants={container} className={className}>
      {children}
    </motion.div>
  );
}

export function Reveal({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <motion.div variants={item} className={className}>
      {children}
    </motion.div>
  );
}
