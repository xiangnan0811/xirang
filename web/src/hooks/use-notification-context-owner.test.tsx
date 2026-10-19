import { StrictMode, useLayoutEffect } from "react";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import type { AuthRole } from "@/context/auth-context.shared";
import { beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier, releaseAuthTransitionBarrier, rememberAuthIdentity } from "@/lib/api/core";
import { useNotificationContextOwner } from "./use-notification-context-owner";

const auth = vi.hoisted(() => ({ token: "A" as string | null, role: "admin" as AuthRole | null }));
vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => auth }));
beforeEach(() => {
  clearAuthTransitionBarrier();
  auth.token = "A";
  auth.role = "admin";
  rememberAuthIdentity("A", "admin");
  bumpAuthSessionGeneration();
});

it("does not resurrect a first StrictMode lifetime after cleanup and setup", () => {
  const callbacks: Array<() => boolean> = [];
  const view = renderHook(() => {
    const owner = useNotificationContextOwner();
    const { capture } = owner;
    useLayoutEffect(() => { callbacks.push(capture()); }, [capture]);
    return owner;
  }, { wrapper: StrictMode });
  expect(callbacks).toHaveLength(2);
  expect(callbacks[0]()).toBe(false);
  expect(callbacks[1]()).toBe(true);
  view.unmount();
  expect(callbacks[1]()).toBe(false);
});

it("disables the surface during a barrier without reviving captured operations on release", () => {
  const view = renderHook(useNotificationContextOwner);
  const before = view.result.current.capture();
  let barrier = 0;
  act(() => { barrier = beginAuthTransitionBarrier(); });
  expect(view.result.current.active).toBe(false);
  expect(before()).toBe(false);
  act(() => releaseAuthTransitionBarrier(barrier));
  expect(view.result.current.active).toBe(true);
  expect(before()).toBe(false);
  expect(view.result.current.capture()()).toBe(true);
});

it("refuses a stale capture factory and mismatched Context even for same-token login", () => {
  const view = renderHook(useNotificationContextOwner);
  const staleFactory = view.result.current.capture;
  act(() => { bumpAuthSessionGeneration(); });
  expect(staleFactory()()).toBe(false);
  const current = view.result.current.capture();
  auth.role = "viewer";
  view.rerender();
  expect(view.result.current.active).toBe(false);
  expect(current()).toBe(false);
});
