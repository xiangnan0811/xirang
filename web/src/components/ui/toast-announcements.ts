const ANNOUNCEMENT_CLEAR_DELAY_MS = 100;
const ANNOUNCEMENT_GAP_MS = 300;

type Snapshot = { text: string; lang: string };
type TrackedToast = { observed: string; lastPublished: string; pending?: Snapshot };

/** Bridge Sonner's visible content to one complete, application-owned live message. */
export function observeToastAnnouncements(
  visualRegion: HTMLElement,
  announcementRegion: HTMLElement,
): () => void {
  const tracked = new Map<HTMLElement, TrackedToast>();
  const queue = new Set<HTMLElement>();
  let stopped = false;
  let timer: number | undefined;
  let publishing: HTMLElement | null = null;
  let displayed: HTMLElement | null = null;
  let lastPublishedAt: number | null = null;

  function clearDisplay() {
    announcementRegion.textContent = "";
    displayed = null;
  }

  function cancelPending(node: HTMLElement, state: TrackedToast) {
    state.pending = undefined;
    queue.delete(node);
    if (publishing === node) {
      window.clearTimeout(timer);
      timer = undefined;
      publishing = null;
    }
  }

  function scan() {
    const present = new Set<HTMLElement>();
    // Each Sonner position is newest-first. Positions retain their DOM order.
    for (const list of visualRegion.querySelectorAll("[data-sonner-toaster]")) {
      const nodes = list.querySelectorAll<HTMLElement>(":scope > [data-sonner-toast]");
      for (let index = nodes.length - 1; index >= 0; index--) {
        const node = nodes[index];
        if (!visualRegion.contains(node) || node.dataset.removed === "true") continue;
        present.add(node);
        const content = node.querySelector(":scope > [data-content]");
        const text = ["[data-title]", "[data-description]"]
          .map((selector) => content?.querySelector(`:scope > ${selector}`)?.textContent?.replace(/\s+/g, " ").trim() ?? "")
          .filter(Boolean)
          .join(" ");
        let state = tracked.get(node);
        if (!state) {
          state = { observed: "", lastPublished: "" };
          tracked.set(node, state);
        }
        if (!text && displayed === node) clearDisplay();
        if (text === state.observed) continue;
        state.observed = text;
        if (!text || text === state.lastPublished) {
          cancelPending(node, state);
        } else {
          state.pending = { text, lang: document.documentElement.lang };
          queue.add(node);
        }
      }
    }
    for (const [node, state] of tracked) {
      if (present.has(node)) continue;
      cancelPending(node, state);
      tracked.delete(node);
      if (displayed === node) clearDisplay();
    }
    if (!queue.size && timer !== undefined) {
      window.clearTimeout(timer);
      timer = undefined;
      publishing = null;
    }
  }

  function schedule() {
    if (stopped || timer !== undefined || !queue.size) return;
    const remaining = lastPublishedAt === null ? 0 : ANNOUNCEMENT_GAP_MS - (performance.now() - lastPublishedAt);
    if (remaining > 0) {
      timer = window.setTimeout(() => {
        if (stopped) return;
        timer = undefined;
        scan();
        schedule();
      }, remaining);
      return;
    }
    const node = queue.values().next().value;
    if (!node) return;
    publishing = node;
    clearDisplay();
    timer = window.setTimeout(() => {
      if (stopped) return;
      timer = undefined;
      publishing = null;
      // Re-read before publishing: removal or an update may precede observer delivery.
      scan();
      const state = tracked.get(node);
      if (state?.pending && visualRegion.contains(node) && node.dataset.removed !== "true") {
        const snapshot = state.pending;
        announcementRegion.lang = snapshot.lang;
        announcementRegion.textContent = snapshot.text;
        displayed = node;
        state.lastPublished = snapshot.text;
        state.pending = undefined;
        queue.delete(node);
        lastPublishedAt = performance.now();
      }
      schedule();
    }, ANNOUNCEMENT_CLEAR_DELAY_MS);
  }

  clearDisplay();
  const observer = new MutationObserver(() => {
    if (stopped) return;
    scan();
    schedule();
  });
  observer.observe(visualRegion, {
    childList: true,
    subtree: true,
    characterData: true,
    attributes: true,
    attributeFilter: ["data-removed"],
  });
  scan();
  schedule();

  return () => {
    if (stopped) return;
    stopped = true;
    observer.disconnect();
    window.clearTimeout(timer);
    timer = undefined;
    publishing = null;
    tracked.clear();
    queue.clear();
    clearDisplay();
  };
}
