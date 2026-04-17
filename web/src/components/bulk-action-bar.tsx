import * as React from "react";
import { AnimatePresence, motion, useReducedMotion } from "framer-motion";

export function BulkActionBar({
  visible,
  children,
}: {
  visible: boolean;
  children: React.ReactNode;
}) {
  const reduced = useReducedMotion();
  return (
    <AnimatePresence>
      {visible ? (
        <motion.div
          initial={reduced ? { opacity: 0 } : { y: 40, opacity: 0 }}
          animate={{ y: 0, opacity: 1 }}
          exit={reduced ? { opacity: 0 } : { y: 40, opacity: 0 }}
          transition={{ duration: 0.18, ease: [0, 0, 0.2, 1] }}
          className="sticky bottom-4 z-40 mx-auto flex w-fit items-center gap-3 rounded-full bg-foreground px-4 py-2 text-background shadow-lg"
        >
          {children}
        </motion.div>
      ) : null}
    </AnimatePresence>
  );
}
