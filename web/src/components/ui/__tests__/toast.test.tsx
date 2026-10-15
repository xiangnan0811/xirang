import { StrictMode } from "react";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { toast as directToast } from "sonner";
import { toast } from "@/components/ui/toast-sonner";
import { Toaster } from "@/components/ui/toast";
import { ThemeProvider } from "@/context/theme-context-provider";
import { useTheme } from "@/context/theme-context.hooks";
import { setLanguage } from "@/i18n";

function ThemeControls() {
  const { setTheme } = useTheme();
  return (
    <>
      <button onClick={() => setTheme("light")}>Light</button>
      <button onClick={() => setTheme("dark")}>Dark</button>
    </>
  );
}

function renderToaster() {
  return render(
    <StrictMode>
      <ThemeProvider>
        <ThemeControls />
        <Toaster />
      </ThemeProvider>
    </StrictMode>,
  );
}

function installSystemTheme(initialDark: boolean) {
  const changes = new EventTarget();
  let dark = initialDark;
  vi.stubGlobal("matchMedia", (query: string) => ({
    media: query,
    get matches() {
      return query === "(prefers-color-scheme: dark)" && dark;
    },
    addEventListener: changes.addEventListener.bind(changes),
    removeEventListener: changes.removeEventListener.bind(changes),
    addListener: (listener: EventListener) => changes.addEventListener("change", listener),
    removeListener: (listener: EventListener) => changes.removeEventListener("change", listener),
  }));
  return (nextDark: boolean) => {
    dark = nextDark;
    const event = new Event("change");
    Object.defineProperty(event, "matches", { value: dark });
    changes.dispatchEvent(event);
  };
}

async function showToast() {
  act(() => {
    toast.success("Saved", { id: "toast-regression", duration: Infinity });
  });
  const region = screen.getByRole("region", { name: "通知（Alt+T）" });
  await within(region).findByText("Saved");
  return within(region).getByText("Saved").closest("[data-sonner-toaster]");
}

beforeEach(async () => {
  localStorage.clear();
  await setLanguage("zh");
});

afterEach(async () => {
  act(() => { toast.dismiss(); });
  cleanup();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  localStorage.clear();
  document.documentElement.classList.remove("dark");
  await setLanguage("zh");
});

describe("Toaster", () => {
  it.each([
    { application: "light", systemDark: true },
    { application: "dark", systemDark: false },
  ])("keeps explicit $application theme when the OS disagrees", async ({ application, systemDark }) => {
    const changeSystemTheme = installSystemTheme(systemDark);
    localStorage.setItem("xirang-theme", application);
    renderToaster();
    const toaster = await showToast();

    expect(toaster).toHaveAttribute("data-sonner-theme", application);
    act(() => { changeSystemTheme(!systemDark); });
    act(() => { changeSystemTheme(systemDark); });
    expect(toaster).toHaveAttribute("data-sonner-theme", application);
  });

  it("follows OS changes until a manual selection, including an already visible notification", async () => {
    const changeSystemTheme = installSystemTheme(false);
    const user = userEvent.setup();
    renderToaster();
    const toaster = await showToast();
    expect(toaster).toHaveAttribute("data-sonner-theme", "light");

    act(() => { changeSystemTheme(true); });
    expect(toaster).toHaveAttribute("data-sonner-theme", "dark");
    await user.click(screen.getByRole("button", { name: "Light" }));
    expect(toaster).toHaveAttribute("data-sonner-theme", "light");
    act(() => { changeSystemTheme(false); });
    act(() => { changeSystemTheme(true); });
    expect(toaster).toHaveAttribute("data-sonner-theme", "light");
    await user.click(screen.getByRole("button", { name: "Dark" }));
    expect(toaster).toHaveAttribute("data-sonner-theme", "dark");
  });

  it("has one polite announcement owner and keeps localized keyboard dismissal available", async () => {
    installSystemTheme(false);
    const user = userEvent.setup();
    renderToaster();
    await showToast();
    const announcement = screen.getByRole("status");
    const region = screen.getByRole("region", { name: "通知（Alt+T）" });
    await waitFor(() => expect(announcement).toHaveTextContent(/^Saved$/));
    expect(region).toHaveAttribute("aria-live", "off");
    expect(document.querySelectorAll('[aria-live="polite"]')).toHaveLength(1);
    expect(screen.getByRole("button", { name: "关闭通知" })).toBeEnabled();

    await act(async () => { await setLanguage("en"); });
    expect(screen.getByRole("region", { name: "Notifications (Alt+T)" })).toBe(region);
    const dismiss = screen.getByRole("button", { name: "Dismiss notification" });
    dismiss.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(within(region).queryByText("Saved")).not.toBeInTheDocument());
    expect(announcement).toBeEmptyDOMElement();
  });

  it.each([toast, directToast])("announces complete rendered content through either producer entry", async (producer) => {
    installSystemTheme(false);
    renderToaster();
    const announcement = screen.getByRole("status");
    expect(announcement).toBeEmptyDOMElement();
    const outputs: string[] = [];
    const observer = new MutationObserver(() => {
      if (announcement.textContent) outputs.push(announcement.textContent);
    });
    observer.observe(announcement, { childList: true, subtree: true, characterData: true });
    try {
      act(() => {
        producer.success("Complete title", {
          description: "Complete description.",
          duration: Infinity,
          icon: <span>Decorative icon</span>,
          action: { label: "Action", onClick: vi.fn() },
          cancel: { label: "Cancel", onClick: vi.fn() },
        });
      });
      const region = screen.getByRole("region", { name: "通知（Alt+T）" });
      await within(region).findByText("Complete description.");
      await waitFor(() => expect(outputs).toEqual(["Complete title Complete description."]));
      expect(region).toHaveAttribute("aria-live", "off");
      expect(announcement).toHaveAttribute("aria-atomic", "true");
      expect(document.querySelectorAll('[aria-live="polite"]')).toHaveLength(1);
    } finally {
      observer.disconnect();
    }
  });
});

describe("toast announcement scheduling", () => {
  let outputs: string[];
  let announcement: HTMLElement;
  let observer: MutationObserver;

  beforeEach(() => {
    installSystemTheme(false);
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "performance", "requestAnimationFrame", "cancelAnimationFrame"],
    });
    renderToaster();
    announcement = screen.getByRole("status");
    outputs = [];
    observer = new MutationObserver(() => {
      if (announcement.textContent) outputs.push(announcement.textContent);
    });
    observer.observe(announcement, { childList: true, subtree: true, characterData: true });
  });

  afterEach(() => { observer.disconnect(); });

  async function advance(milliseconds: number) {
    // Let real MutationObserver delivery and React commits settle between timer phases.
    for (let elapsed = 0; elapsed < milliseconds; elapsed += 10) {
      await act(async () => { await vi.advanceTimersByTimeAsync(Math.min(10, milliseconds - elapsed)); });
    }
  }

  async function notify(id: string, title: string, description?: string) {
    act(() => { toast(title, { id, description, duration: Infinity }); });
    await advance(20);
  }

  it("announces only new stack entries, preserving FIFO and separate identical messages", async () => {
    await notify("first", "Earlier", "Keep visible.");
    await advance(120);
    expect(outputs).toEqual(["Earlier Keep visible."]);
    act(() => {
      toast("Second", { id: "second", description: "Next.", duration: Infinity });
      toast("Repeated", { id: "repeat-a", description: "Same.", duration: Infinity });
      directToast("Repeated", { id: "repeat-b", description: "Same.", duration: Infinity });
    });
    await advance(1500);
    expect(outputs).toEqual(["Earlier Keep visible.", "Second Next.", "Repeated Same.", "Repeated Same."]);
    expect(screen.getByRole("region")).toHaveTextContent("Earlier");
  });

  it("coalesces updates before publication and reads the latest clear-phase snapshot", async () => {
    await notify("update", "Operation", "Initial.");
    await notify("update", "Operation", "Intermediate.");
    await notify("update", "Operation", "Latest.");
    await advance(120);
    expect(outputs).toEqual(["Operation Latest."]);
    await notify("update", "Operation", "Pending.");
    await advance(250);
    expect(announcement).toBeEmptyDOMElement();
    await notify("update", "Operation", "Finished.");
    await advance(150);
    expect(outputs).toEqual(["Operation Latest.", "Operation Finished."]);
  });

  it.each([20, 280])("cancels an update reverting to published text after %i ms", async (delay) => {
    await notify("revert", "Stable", "Published.");
    await advance(120);
    await notify("revert", "Stable", "Transient.");
    await advance(delay);
    await notify("revert", "Stable", "Published.");
    await advance(600);
    expect(outputs).toEqual(["Stable Published."]);
  });

  it("keeps pending queue position when a later snapshot replaces it", async () => {
    await notify("anchor", "Anchor");
    await advance(120);
    await notify("pending", "Old");
    await notify("later", "Later");
    await notify("pending", "Replacement");
    await advance(1000);
    expect(outputs).toEqual(["Anchor", "Replacement", "Later"]);
  });

  it("ignores theme and label rerenders while capturing the next message language", async () => {
    await notify("localized", "Original", "Content.");
    await advance(120);
    expect(announcement).toHaveAttribute("lang", "zh-CN");
    act(() => { screen.getByRole("button", { name: "Dark" }).click(); });
    await act(async () => { await setLanguage("en"); });
    await advance(500);
    expect(outputs).toEqual(["Original Content."]);
    expect(screen.getByRole("region", { name: "Notifications (Alt+T)" })).toHaveAttribute("aria-live", "off");
    expect(screen.getByRole("button", { name: "Dismiss notification" })).toBeEnabled();
    await notify("english", "English", "New content.");
    await advance(120);
    expect(outputs).toEqual(["Original Content.", "English New content."]);
    expect(announcement).toHaveAttribute("lang", "en");
  });

  it.each([20, 260])("does not publish a source dismissed after %i ms of waiting", async (delay) => {
    await notify("anchor", "Anchor");
    await advance(120);
    await notify("dismissed", "Must not speak");
    await notify("survivor", "Survivor");
    await advance(delay);
    if (delay === 260) expect(announcement).toBeEmptyDOMElement();
    act(() => { toast.dismiss("dismissed"); });
    await advance(1000);
    expect(outputs).toEqual(["Anchor", "Survivor"]);
  });

  it("ignores empty content and clears an emptied or removed displayed source", async () => {
    await notify("empty", " ", " \n ");
    await advance(120);
    expect(outputs).toEqual([]);
    await notify("content", "Title", "");
    await advance(120);
    expect(outputs).toEqual(["Title"]);
    await notify("content", "", "");
    await advance(500);
    expect(announcement).toBeEmptyDOMElement();
    expect(outputs).toEqual(["Title"]);
    await notify("removed", "Remove me");
    await advance(120);
    act(() => { toast.dismiss("removed"); });
    await advance(500);
    expect(announcement).toBeEmptyDOMElement();
    expect(outputs).toEqual(["Title", "Remove me"]);
  });

  it("cancels pending work on unmount and allows a fresh StrictMode instance to publish once", async () => {
    await notify("unmounted", "Never published");
    const oldAnnouncement = announcement;
    cleanup();
    act(() => { toast.dismiss(); });
    await advance(600);
    expect(outputs).toEqual([]);
    expect(oldAnnouncement).toBeEmptyDOMElement();
    observer.disconnect();
    renderToaster();
    announcement = screen.getByRole("status");
    observer.observe(announcement, { childList: true, subtree: true, characterData: true });
    await notify("remounted", "Fresh", "Instance.");
    await advance(1000);
    expect(outputs).toEqual(["Fresh Instance."]);
    expect(oldAnnouncement).toBeEmptyDOMElement();
  });
});
